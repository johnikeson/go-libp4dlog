/*
Metrics module - consume parsed p4d log commands and produce metrics

Primary processing for p4prometheus module.

Also used in log2sql for historical metrics.
*/

package metrics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	p4dlog "github.com/rcowham/go-libp4dlog"
	"github.com/sirupsen/logrus"
)

// NotLabelValueRE - any chars in label values not matching this will be converted to underscores.
// We exclude chars such as: <space>;!="^'
// Allowed values must be valid for node_exporter and also the graphite text protocol for labels/tags
// https://graphite.readthedocs.io/en/latest/tags.html
// In addition any backslashes must be double quoted for node_exporter.
var NotLabelValueRE = regexp.MustCompile(`[^a-zA-Z0-9_/+:@{}&%<>*\\.,\(\)\[\]-]`)

// Config for metrics
type Config struct {
	Debug                 int           `yaml:"debug"`
	ServerID              string        `yaml:"server_id"`
	SDPInstance           string        `yaml:"sdp_instance"`
	UpdateInterval        time.Duration `yaml:"update_interval"`
	OutputCmdsByUser      bool          `yaml:"output_cmds_by_user"`
	OutputCmdsByUserRegex string        `yaml:"output_cmds_by_user_regex"`
	OutputCmdsByIP        bool          `yaml:"output_cmds_by_ip"`
	CaseSensitiveServer   bool          `yaml:"case_sensitive_server"`
}

// P4DMetrics structure
type P4DMetrics struct {
	config                    *Config
	historical                bool
	debug                     int
	fp                        *p4dlog.P4dFileParser
	timeLatestStartCmd        time.Time
	latestStartCmdBuf         string
	logger                    *logrus.Logger
	metricWriter              io.Writer
	timeChan                  chan time.Time
	cmdRunning                int64
	cmdCounter                map[string]int64
	cmdErrorCounter           map[string]int64
	cmdCumulative             map[string]float64
	cmduCPUCumulative         map[string]float64
	cmdsCPUCumulative         map[string]float64
	cmdByUserCounter          map[string]int64
	cmdByUserCumulative       map[string]float64
	cmdByIPCounter            map[string]int64
	cmdByIPCumulative         map[string]float64
	cmdByReplicaCounter       map[string]int64
	cmdByReplicaCumulative    map[string]float64
	cmdByProgramCounter       map[string]int64
	cmdByProgramCumulative    map[string]float64
	cmdByUserDetailCounter    map[string]map[string]int64
	cmdByUserDetailCumulative map[string]map[string]float64
	totalReadWait             map[string]float64
	totalReadHeld             map[string]float64
	totalWriteWait            map[string]float64
	totalWriteHeld            map[string]float64
	totalTriggerLapse         map[string]float64
	syncFilesAdded            int64
	syncFilesUpdated          int64
	syncFilesDeleted          int64
	syncBytesAdded            int64
	syncBytesUpdated          int64
	cmdsProcessed             int64
	linesRead                 int64
	LbrRcsOpens               int64
	LbrRcsCloses              int64
	LbrRcsCheckins            int64
	LbrRcsExists              int64
	LbrRcsReads               int64
	LbrRcsReadBytes           int64
	LbrRcsWrites              int64
	LbrRcsWriteBytes          int64
	LbrRcsDigests             int64
	LbrRcsFileSizes           int64
	LbrRcsModTimes            int64
	LbrRcsCopies              int64
	LbrCompressOpens          int64
	LbrCompressCloses         int64
	LbrCompressCheckins       int64
	LbrCompressExists         int64
	LbrCompressReads          int64
	LbrCompressReadBytes      int64
	LbrCompressWrites         int64
	LbrCompressWriteBytes     int64
	LbrCompressDigests        int64
	LbrCompressFileSizes      int64
	LbrCompressModTimes       int64
	LbrCompressCopies         int64
	LbrUncompressOpens        int64
	LbrUncompressCloses       int64
	LbrUncompressCheckins     int64
	LbrUncompressExists       int64
	LbrUncompressReads        int64
	LbrUncompressReadBytes    int64
	LbrUncompressWrites       int64
	LbrUncompressWriteBytes   int64
	LbrUncompressDigests      int64
	LbrUncompressFileSizes    int64
	LbrUncompressModTimes     int64
	LbrUncompressCopies       int64
	outputCmdsByUserRegex     *regexp.Regexp
}

// NewP4DMetricsLogParser - wraps P4dFileParser
func NewP4DMetricsLogParser(config *Config, logger *logrus.Logger, historical bool) *P4DMetrics {
	return &P4DMetrics{
		config:                    config,
		logger:                    logger,
		fp:                        p4dlog.NewP4dFileParser(logger),
		historical:                historical,
		cmdCounter:                make(map[string]int64),
		cmdErrorCounter:           make(map[string]int64),
		cmdCumulative:             make(map[string]float64),
		cmduCPUCumulative:         make(map[string]float64),
		cmdsCPUCumulative:         make(map[string]float64),
		cmdByUserCounter:          make(map[string]int64),
		cmdByUserCumulative:       make(map[string]float64),
		cmdByIPCounter:            make(map[string]int64),
		cmdByIPCumulative:         make(map[string]float64),
		cmdByReplicaCounter:       make(map[string]int64),
		cmdByReplicaCumulative:    make(map[string]float64),
		cmdByProgramCounter:       make(map[string]int64),
		cmdByProgramCumulative:    make(map[string]float64),
		cmdByUserDetailCounter:    make(map[string]map[string]int64),
		cmdByUserDetailCumulative: make(map[string]map[string]float64),
		totalReadWait:             make(map[string]float64),
		totalReadHeld:             make(map[string]float64),
		totalWriteWait:            make(map[string]float64),
		totalWriteHeld:            make(map[string]float64),
		totalTriggerLapse:         make(map[string]float64),
	}
}

// SetDebugPID - for debug purposes
func (p4m *P4DMetrics) SetDebugPID(pid int64, cmdName string) {
	p4m.fp.SetDebugPID(pid, cmdName)
}

// SetDebugMode - for debug purposes
func (p4m *P4DMetrics) SetDebugMode(level int) {
	p4m.debug = level
	p4m.fp.SetDebugMode(level)
}

// defines metrics label
type labelStruct struct {
	name  string
	value string
}

func (p4m *P4DMetrics) printMetricHeader(f io.Writer, name string, help string, metricType string) {
	if !p4m.historical {
		fmt.Fprintf(f, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
	}
}

// Prometheus format: 	metric_name{label1="val1",label2="val2"}
// Graphite format:  	metric_name;label1=val1;label2=val2
func (p4m *P4DMetrics) formatLabels(mname string, labels []labelStruct) string {
	nonBlankLabels := make([]labelStruct, 0)
	for _, l := range labels {
		if l.value != "" {
			if !p4m.historical {
				l.value = fmt.Sprintf("\"%s\"", l.value)
			}
			nonBlankLabels = append(nonBlankLabels, l)
		}
	}
	vals := make([]string, 0)
	for _, l := range nonBlankLabels {
		vals = append(vals, fmt.Sprintf("%s=%s", l.name, l.value))
	}
	if p4m.historical {
		labelStr := strings.Join(vals, ";")
		if len(labelStr) > 0 {
			return fmt.Sprintf("%s;%s", mname, labelStr)
		}
		return fmt.Sprintf("%s", mname)
	}
	labelStr := strings.Join(vals, ",")
	return fmt.Sprintf("%s{%s}", mname, labelStr)
}

func (p4m *P4DMetrics) formatMetric(mname string, labels []labelStruct, metricVal string) string {
	if p4m.historical {
		return fmt.Sprintf("%s %s %d\n", p4m.formatLabels(mname, labels),
			metricVal, p4m.timeLatestStartCmd.Unix())
	}
	return fmt.Sprintf("%s %s\n", p4m.formatLabels(mname, labels), metricVal)
}

func (p4m *P4DMetrics) printMetric(metrics *bytes.Buffer, mname string, labels []labelStruct, metricVal string) {
	buf := p4m.formatMetric(mname, labels, metricVal)
	if p4dlog.FlagSet(p4m.debug, p4dlog.DebugMetricStats) {
		p4m.logger.Debugf(buf)
	}
	// node_exporter requires doubling of backslashes
	buf = strings.Replace(buf, `\`, "\\\\", -1)
	fmt.Fprint(metrics, buf)
}

// Publish cumulative results - called on a ticker or in historical mode
func (p4m *P4DMetrics) getCumulativeMetrics() string {
	fixedLabels := []labelStruct{{name: "serverid", value: p4m.config.ServerID},
		{name: "sdpinst", value: p4m.config.SDPInstance}}
	metrics := new(bytes.Buffer)
	if p4dlog.FlagSet(p4m.debug, p4dlog.DebugMetricStats) {
		p4m.logger.Debugf("Writing stats")
	}

	var mname string
	var metricVal string
	mname = "p4_prom_log_lines_read"
	p4m.printMetricHeader(metrics, mname, "A count of log lines read", "counter")
	metricVal = fmt.Sprintf("%d", p4m.linesRead)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_prom_cmds_processed"
	p4m.printMetricHeader(metrics, mname, "A count of all cmds processed", "counter")
	metricVal = fmt.Sprintf("%d", p4m.cmdsProcessed)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_prom_cmds_pending"
	p4m.printMetricHeader(metrics, mname, "A count of all current cmds (not completed)", "gauge")
	metricVal = fmt.Sprintf("%d", p4m.fp.CmdsPendingCount())
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_cmd_running"
	p4m.printMetricHeader(metrics, mname, "The number of running commands at any one time", "gauge")
	metricVal = fmt.Sprintf("%d", p4m.cmdRunning)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	// Cross platform call - eventually when Windows implemented
	userCPU, systemCPU := getCPUStats()
	mname = "p4_prom_cpu_user"
	p4m.printMetricHeader(metrics, mname, "User CPU used by p4prometheus", "counter")
	metricVal = fmt.Sprintf("%.6f", userCPU)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_prom_cpu_system"
	p4m.printMetricHeader(metrics, mname, "System CPU used by p4prometheus", "counter")
	metricVal = fmt.Sprintf("%.6f", systemCPU)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_sync_files_added"
	p4m.printMetricHeader(metrics, mname, "The number of files added to workspaces by syncs", "counter")
	metricVal = fmt.Sprintf("%d", p4m.syncFilesAdded)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_sync_files_updated"
	p4m.printMetricHeader(metrics, mname, "The number of files updated in workspaces by syncs", "counter")
	metricVal = fmt.Sprintf("%d", p4m.syncFilesUpdated)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_sync_files_deleted"
	p4m.printMetricHeader(metrics, mname, "The number of files deleted in workspaces by syncs", "counter")
	metricVal = fmt.Sprintf("%d", p4m.syncFilesDeleted)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_sync_bytes_added"
	p4m.printMetricHeader(metrics, mname, "The number of bytes added to workspaces by syncs", "counter")
	metricVal = fmt.Sprintf("%d", p4m.syncBytesAdded)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_sync_bytes_updated"
	p4m.printMetricHeader(metrics, mname, "The number of bytes updated in workspaces by syncs", "counter")
	metricVal = fmt.Sprintf("%d", p4m.syncBytesUpdated)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_rcs_opens"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Rcs opens for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrRcsOpens)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_rcs_closes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Rcs closes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrRcsCloses)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_rcs_exists"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Rcs exists for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrRcsExists)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_rcs_checkins"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Rcs Checkins for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrRcsCheckins)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_rcs_reads"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Rcs Reads for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrRcsReads)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_rcs_readbytes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Rcs ReadBytes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrRcsReadBytes)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_rcs_writes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Rcs Writes updated in workspaces by syncs", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrRcsWrites)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_rcs_writebytes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Rcs WriteBytes updated in workspaces by syncs", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrRcsWriteBytes)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_rcs_digests"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Rcs Digests for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrRcsDigests)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_rcs_filesizes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Rcs FileSizes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrRcsFileSizes)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_rcs_modtimes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Rcs ModTimes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrRcsModTimes)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_rcs_copies"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Rcs Copies for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrRcsCopies)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_compress_opens"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Compress Opens for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrCompressOpens)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_compress_closes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Compress Closes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrCompressCloses)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_compress_exists"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Compress Exists for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrCompressExists)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_compress_checkins"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Compress Checkins for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrCompressCheckins)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_compress_reads"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Compress Reads for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrCompressReads)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_compress_readbytes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Compress ReadBytes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrCompressReadBytes)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_compress_writes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Compress Writes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrCompressWrites)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_compress_writebytes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Compress WriteBytes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrCompressWriteBytes)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_compress_digests"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Compress Digests for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrCompressDigests)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_compress_filesizes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Compress FileSizes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrCompressFileSizes)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_compress_modtimes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Compress ModTimes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrCompressModTimes)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_compress_copies"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Compress Copies for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrCompressCopies)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_uncompress_opens"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Uncompress Opens for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrUncompressOpens)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_uncompress_closes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Uncompress Closes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrUncompressCloses)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_uncompress_exists"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Uncompress Exists for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrUncompressExists)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_uncompress_checkins"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Uncompress Checkins for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrUncompressCheckins)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_uncompress_reads"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Uncompress Reads for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrUncompressReads)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_uncompress_readbytes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Uncompress ReadBytes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrUncompressReadBytes)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_uncompress_writes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Uncompress Writes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrUncompressWrites)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_uncompress_writebytes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Uncompress WriteBytes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrUncompressWriteBytes)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_uncompress_digests"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Uncompress Digests for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrUncompressDigests)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_uncompress_filesizes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Uncompress FileSizes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrUncompressFileSizes)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_uncompress_modtimes"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Uncompress ModTimes for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrUncompressModTimes)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_lbr_uncompress_copies"
	p4m.printMetricHeader(metrics, mname, "The number of Lbr Uncompress Copies for commands", "counter")
	metricVal = fmt.Sprintf("%d", p4m.LbrUncompressCopies)
	p4m.printMetric(metrics, mname, fixedLabels, metricVal)

	mname = "p4_cmd_counter"
	p4m.printMetricHeader(metrics, mname, "A count of completed p4 cmds (by cmd)", "counter")
	for cmd, count := range p4m.cmdCounter {
		metricVal = fmt.Sprintf("%d", count)
		labels := append(fixedLabels, labelStruct{"cmd", cmd})
		p4m.printMetric(metrics, mname, labels, metricVal)
	}
	mname = "p4_cmd_cumulative_seconds"
	p4m.printMetricHeader(metrics, mname, "The total in seconds (by cmd)", "counter")
	for cmd, lapse := range p4m.cmdCumulative {
		metricVal = fmt.Sprintf("%0.3f", lapse)
		labels := append(fixedLabels, labelStruct{"cmd", cmd})
		p4m.printMetric(metrics, mname, labels, metricVal)
	}
	mname = "p4_cmd_cpu_user_cumulative_seconds"
	p4m.printMetricHeader(metrics, mname, "The total in user CPU seconds (by cmd)", "counter")
	for cmd, lapse := range p4m.cmduCPUCumulative {
		metricVal = fmt.Sprintf("%0.3f", lapse)
		labels := append(fixedLabels, labelStruct{"cmd", cmd})
		p4m.printMetric(metrics, mname, labels, metricVal)
	}
	mname = "p4_cmd_cpu_system_cumulative_seconds"
	p4m.printMetricHeader(metrics, mname, "The total in system CPU seconds (by cmd)", "counter")
	for cmd, lapse := range p4m.cmdsCPUCumulative {
		metricVal = fmt.Sprintf("%0.3f", lapse)
		labels := append(fixedLabels, labelStruct{"cmd", cmd})
		p4m.printMetric(metrics, mname, labels, metricVal)
	}
	mname = "p4_cmd_error_counter"
	p4m.printMetricHeader(metrics, mname, "A count of cmd errors (by cmd)", "counter")
	for cmd, count := range p4m.cmdErrorCounter {
		metricVal = fmt.Sprintf("%d", count)
		labels := append(fixedLabels, labelStruct{"cmd", cmd})
		p4m.printMetric(metrics, mname, labels, metricVal)
	}
	// For large sites this might not be sensible - so they can turn it off
	if p4m.config.OutputCmdsByUser {
		mname = "p4_cmd_user_counter"
		p4m.printMetricHeader(metrics, mname, "A count of completed p4 cmds (by user)", "counter")
		for user, count := range p4m.cmdByUserCounter {
			metricVal = fmt.Sprintf("%d", count)
			labels := append(fixedLabels, labelStruct{"user", user})
			p4m.printMetric(metrics, mname, labels, metricVal)
		}
		mname = "p4_cmd_user_cumulative_seconds"
		p4m.printMetricHeader(metrics, mname, "The total in seconds (by user)", "counter")
		for user, lapse := range p4m.cmdByUserCumulative {
			metricVal = fmt.Sprintf("%0.3f", lapse)
			labels := append(fixedLabels, labelStruct{"user", user})
			p4m.printMetric(metrics, mname, labels, metricVal)
		}
	}
	// For large sites this might not be sensible - so they can turn it off
	if p4m.config.OutputCmdsByIP {
		mname = "p4_cmd_ip_counter"
		p4m.printMetricHeader(metrics, mname, "A count of completed p4 cmds (by IP)", "counter")
		for ip, count := range p4m.cmdByIPCounter {
			metricVal = fmt.Sprintf("%d", count)
			labels := append(fixedLabels, labelStruct{"ip", ip})
			p4m.printMetric(metrics, mname, labels, metricVal)
		}
		mname = "p4_cmd_ip_cumulative_seconds"
		p4m.printMetricHeader(metrics, mname, "The total in seconds (by IP)", "counter")
		for ip, lapse := range p4m.cmdByIPCumulative {
			metricVal = fmt.Sprintf("%0.3f", lapse)
			labels := append(fixedLabels, labelStruct{"ip", ip})
			p4m.printMetric(metrics, mname, labels, metricVal)
		}
	}
	// For large sites this might not be sensible - so they can turn it off
	if p4m.config.OutputCmdsByUserRegex != "" {
		mname = "p4_cmd_user_detail_counter"
		p4m.printMetricHeader(metrics, mname, "A count of completed p4 cmds (by user and cmd)", "counter")
		for user, userMap := range p4m.cmdByUserDetailCounter {
			for cmd, count := range userMap {
				metricVal = fmt.Sprintf("%d", count)
				labels := append(fixedLabels, labelStruct{"user", user})
				labels = append(labels, labelStruct{"cmd", cmd})
				p4m.printMetric(metrics, mname, labels, metricVal)
			}
		}
		mname = "p4_cmd_user_detail_cumulative_seconds"
		p4m.printMetricHeader(metrics, mname, "The total in seconds (by user and cmd)", "counter")
		for user, userMap := range p4m.cmdByUserDetailCumulative {
			for cmd, lapse := range userMap {
				metricVal = fmt.Sprintf("%0.3f", lapse)
				labels := append(fixedLabels, labelStruct{"user", user})
				labels = append(labels, labelStruct{"cmd", cmd})
				p4m.printMetric(metrics, mname, labels, metricVal)
			}
		}
	}
	mname = "p4_cmd_replica_counter"
	p4m.printMetricHeader(metrics, mname, "A count of completed p4 cmds (by broker/replica/proxy)", "counter")
	for replica, count := range p4m.cmdByReplicaCounter {
		metricVal = fmt.Sprintf("%d", count)
		labels := append(fixedLabels, labelStruct{"replica", replica})
		p4m.printMetric(metrics, mname, labels, metricVal)
	}
	mname = "p4_cmd_replica_cumulative_seconds"
	p4m.printMetricHeader(metrics, mname, "The total in seconds (by broker/replica/proxy)", "counter")
	for replica, lapse := range p4m.cmdByReplicaCumulative {
		metricVal = fmt.Sprintf("%0.3f", lapse)
		labels := append(fixedLabels, labelStruct{"replica", replica})
		p4m.printMetric(metrics, mname, labels, metricVal)
	}
	mname = "p4_cmd_program_counter"
	p4m.printMetricHeader(metrics, mname, "A count of completed p4 cmds (by program)", "counter")
	for program, count := range p4m.cmdByProgramCounter {
		metricVal = fmt.Sprintf("%d", count)
		labels := append(fixedLabels, labelStruct{"program", program})
		p4m.printMetric(metrics, mname, labels, metricVal)
	}
	mname = "p4_cmd_program_cumulative_seconds"
	p4m.printMetricHeader(metrics, mname, "The total in seconds (by program)", "counter")
	for program, lapse := range p4m.cmdByProgramCumulative {
		metricVal = fmt.Sprintf("%0.3f", lapse)
		labels := append(fixedLabels, labelStruct{"program", program})
		p4m.printMetric(metrics, mname, labels, metricVal)
	}
	mname = "p4_total_read_wait_seconds"
	p4m.printMetricHeader(metrics, mname,
		"The total waiting for read locks in seconds (by table)", "counter")
	for table, total := range p4m.totalReadWait {
		metricVal = fmt.Sprintf("%0.3f", total)
		labels := append(fixedLabels, labelStruct{"table", table})
		p4m.printMetric(metrics, mname, labels, metricVal)
	}
	mname = "p4_total_read_held_seconds"
	p4m.printMetricHeader(metrics, mname,
		"The total read locks held in seconds (by table)", "counter")
	for table, total := range p4m.totalReadHeld {
		metricVal = fmt.Sprintf("%0.3f", total)
		labels := append(fixedLabels, labelStruct{"table", table})
		p4m.printMetric(metrics, mname, labels, metricVal)
	}
	mname = "p4_total_write_wait_seconds"
	p4m.printMetricHeader(metrics, mname,
		"The total waiting for write locks in seconds (by table)", "counter")
	for table, total := range p4m.totalWriteWait {
		metricVal = fmt.Sprintf("%0.3f", total)
		labels := append(fixedLabels, labelStruct{"table", table})
		p4m.printMetric(metrics, mname, labels, metricVal)
	}
	mname = "p4_total_write_held_seconds"
	p4m.printMetricHeader(metrics, mname,
		"The total write locks held in seconds (by table)", "counter")
	for table, total := range p4m.totalWriteHeld {
		metricVal = fmt.Sprintf("%0.3f", total)
		labels := append(fixedLabels, labelStruct{"table", table})
		p4m.printMetric(metrics, mname, labels, metricVal)
	}
	if len(p4m.totalTriggerLapse) > 0 {
		mname = "p4_total_trigger_lapse_seconds"
		p4m.printMetricHeader(metrics, mname,
			"The total lapse time for triggers in seconds (by trigger)", "counter")
		for table, total := range p4m.totalTriggerLapse {
			metricVal = fmt.Sprintf("%0.3f", total)
			labels := append(fixedLabels, labelStruct{"trigger", table})
			p4m.printMetric(metrics, mname, labels, metricVal)
		}
	}
	return metrics.String()
}

func (p4m *P4DMetrics) publishEvent(cmd p4dlog.Command) {
	// p4m.logger.Debugf("publish cmd: %s\n", cmd.String())

	p4m.cmdCounter[cmd.Cmd]++
	p4m.cmdCumulative[cmd.Cmd] += float64(cmd.CompletedLapse)
	p4m.cmduCPUCumulative[cmd.Cmd] += float64(cmd.UCpu) / 1000
	p4m.cmdsCPUCumulative[cmd.Cmd] += float64(cmd.SCpu) / 1000
	if cmd.CmdError {
		p4m.cmdErrorCounter[cmd.Cmd]++
	}
	p4m.cmdRunning = cmd.Running
	p4m.syncFilesAdded += cmd.NetFilesAdded
	p4m.syncFilesUpdated += cmd.NetFilesUpdated
	p4m.syncFilesDeleted += cmd.NetFilesDeleted
	p4m.syncBytesAdded += cmd.NetBytesAdded
	p4m.syncBytesUpdated += cmd.NetBytesUpdated
	p4m.LbrRcsOpens += cmd.LbrRcsOpens
	p4m.LbrRcsCloses += cmd.LbrRcsCloses
	p4m.LbrRcsExists += cmd.LbrRcsExists
	p4m.LbrRcsCheckins += cmd.LbrRcsCheckins
	p4m.LbrRcsReads += cmd.LbrRcsReads
	p4m.LbrRcsReadBytes += cmd.LbrRcsReadBytes
	p4m.LbrRcsWrites += cmd.LbrRcsWrites
	p4m.LbrRcsWriteBytes += cmd.LbrRcsWriteBytes
	p4m.LbrRcsDigests += cmd.LbrRcsDigests
	p4m.LbrRcsFileSizes += cmd.LbrRcsFileSizes
	p4m.LbrRcsModTimes += cmd.LbrRcsModTimes
	p4m.LbrRcsCopies += cmd.LbrRcsCopies
	p4m.LbrCompressOpens += cmd.LbrCompressOpens
	p4m.LbrCompressCloses += cmd.LbrCompressCloses
	p4m.LbrCompressExists += cmd.LbrCompressExists
	p4m.LbrCompressCheckins += cmd.LbrCompressCheckins
	p4m.LbrCompressReads += cmd.LbrCompressReads
	p4m.LbrCompressReadBytes += cmd.LbrCompressReadBytes
	p4m.LbrCompressWrites += cmd.LbrCompressWrites
	p4m.LbrCompressWriteBytes += cmd.LbrCompressWriteBytes
	p4m.LbrCompressDigests += cmd.LbrCompressDigests
	p4m.LbrCompressFileSizes += cmd.LbrCompressFileSizes
	p4m.LbrCompressModTimes += cmd.LbrCompressModTimes
	p4m.LbrCompressCopies += cmd.LbrCompressCopies
	p4m.LbrUncompressOpens += cmd.LbrUncompressOpens
	p4m.LbrUncompressCloses += cmd.LbrUncompressCloses
	p4m.LbrUncompressExists += cmd.LbrUncompressExists
	p4m.LbrUncompressCheckins += cmd.LbrUncompressCheckins
	p4m.LbrUncompressReads += cmd.LbrUncompressReads
	p4m.LbrUncompressReadBytes += cmd.LbrUncompressReadBytes
	p4m.LbrUncompressWrites += cmd.LbrUncompressWrites
	p4m.LbrUncompressWriteBytes += cmd.LbrUncompressWriteBytes
	p4m.LbrUncompressDigests += cmd.LbrUncompressDigests
	p4m.LbrUncompressFileSizes += cmd.LbrUncompressFileSizes
	p4m.LbrUncompressModTimes += cmd.LbrUncompressModTimes
	p4m.LbrUncompressCopies += cmd.LbrUncompressCopies
	user := cmd.User
	if !p4m.config.CaseSensitiveServer {
		user = strings.ToLower(user)
	}
	p4m.cmdByUserCounter[user]++
	p4m.cmdByUserCumulative[user] += float64(cmd.CompletedLapse)
	if p4m.config.OutputCmdsByUserRegex != "" {
		if p4m.outputCmdsByUserRegex == nil {
			regexStr := fmt.Sprintf("(%s)", p4m.config.OutputCmdsByUserRegex)
			p4m.outputCmdsByUserRegex = regexp.MustCompile(regexStr)
		}
		if p4m.outputCmdsByUserRegex.MatchString(user) {
			if _, ok := p4m.cmdByUserDetailCounter[user]; !ok {
				p4m.cmdByUserDetailCounter[user] = make(map[string]int64)
				p4m.cmdByUserDetailCumulative[user] = make(map[string]float64)
			}
			p4m.cmdByUserDetailCounter[user][cmd.Cmd]++
			p4m.cmdByUserDetailCumulative[user][cmd.Cmd] += float64(cmd.CompletedLapse)
		}
	}
	var ip, replica string
	j := strings.Index(cmd.IP, "/")
	if j > 0 {
		replica = cmd.IP[:j]
		ip = cmd.IP[j+1:]
	} else {
		ip = cmd.IP
	}
	p4m.cmdByIPCounter[ip]++
	p4m.cmdByIPCumulative[ip] += float64(cmd.CompletedLapse)
	if replica != "" {
		p4m.cmdByReplicaCounter[replica]++
		p4m.cmdByReplicaCumulative[replica] += float64(cmd.CompletedLapse)
	}
	// Various chars not allowed in label names - see comment for NotLabelValueRE
	program := strings.ReplaceAll(cmd.App, " (brokered)", "")
	program = NotLabelValueRE.ReplaceAllString(program, "_")
	p4m.cmdByProgramCounter[program]++
	p4m.cmdByProgramCumulative[program] += float64(cmd.CompletedLapse)
	const triggerPrefix = "trigger_"

	for _, t := range cmd.Tables {
		if len(t.TableName) > len(triggerPrefix) && t.TableName[:len(triggerPrefix)] == triggerPrefix {
			triggerName := t.TableName[len(triggerPrefix):]
			p4m.totalTriggerLapse[triggerName] += float64(t.TriggerLapse)
		} else {
			p4m.totalReadHeld[t.TableName] += float64(t.TotalReadHeld) / 1000
			p4m.totalReadWait[t.TableName] += float64(t.TotalReadWait) / 1000
			p4m.totalWriteHeld[t.TableName] += float64(t.TotalWriteHeld) / 1000
			p4m.totalWriteWait[t.TableName] += float64(t.TotalWriteWait) / 1000
		}
	}
}

// GO standard reference value/format: Mon Jan 2 15:04:05 -0700 MST 2006
const p4timeformat = "2006/01/02 15:04:05"

// Searches for log lines starting with a <tab>date - assumes increasing dates in log
func (p4m *P4DMetrics) historicalUpdateRequired(line string) bool {
	if !p4m.historical {
		return false
	}
	// This next section is more efficient than regex parsing - we return ASAP
	const lenPrefix = len("\t2020/03/04 12:13:14")
	if len(line) < lenPrefix {
		return false
	}
	// Check for expected chars at specific points
	if line[0] != '\t' || line[5] != '/' || line[8] != '/' ||
		line[11] != ' ' || line[14] != ':' || line[17] != ':' {
		return false
	}
	// Check for digits
	for _, i := range []int{1, 2, 3, 4, 6, 7, 9, 10, 12, 13, 15, 16, 18, 19} {
		if line[i] < byte('0') || line[i] > byte('9') {
			return false
		}
	}
	if len(p4m.latestStartCmdBuf) == 0 {
		p4m.latestStartCmdBuf = line[:lenPrefix]
		p4m.timeLatestStartCmd, _ = time.Parse(p4timeformat, line[1:lenPrefix])
		return false
	}
	if len(p4m.latestStartCmdBuf) > 0 && p4m.latestStartCmdBuf == line[:lenPrefix] {
		return false
	}
	// Update only if greater (due to log format we do see out of sequence dates with track records)
	if strings.Compare(line[:lenPrefix], p4m.latestStartCmdBuf) <= 0 {
		return false
	}
	dt, _ := time.Parse(p4timeformat, string(line[1:lenPrefix]))
	if dt.Sub(p4m.timeLatestStartCmd) >= 3*time.Second {
		p4m.timeChan <- dt
	}
	if dt.Sub(p4m.timeLatestStartCmd) >= p4m.config.UpdateInterval {
		p4m.timeLatestStartCmd = dt
		p4m.latestStartCmdBuf = line[:lenPrefix]
		return true
	}
	return false
}

// ProcessEvents - main event loop for P4Prometheus - reads lines and outputs metrics
// Wraps p4dlog.LogParser event loop
func (p4m *P4DMetrics) ProcessEvents(ctx context.Context, linesInChan <-chan string, needCmdChan bool) (
	chan p4dlog.Command, chan string) {
	ticker := time.NewTicker(p4m.config.UpdateInterval)

	if p4m.config.Debug > 0 {
		p4m.fp.SetDebugMode(p4m.config.Debug)
	}
	fpLinesChan := make(chan string, 10000)
	// Leave as unset
	if p4m.historical {
		p4m.timeChan = make(chan time.Time, 1000)
	}

	metricsChan := make(chan string, 1000)
	var cmdsOutChan chan p4dlog.Command
	if needCmdChan {
		cmdsOutChan = make(chan p4dlog.Command, 10000)
	}
	cmdsInChan := p4m.fp.LogParser(ctx, fpLinesChan, p4m.timeChan)

	go func() {
		defer close(metricsChan)
		if needCmdChan {
			defer close(cmdsOutChan)
		}
		for {
			select {
			case <-ctx.Done():
				p4m.logger.Info("Done received")
				return
			case <-ticker.C:
				// Ticker only relevant for live log processing
				if p4dlog.FlagSet(p4m.debug, p4dlog.DebugMetricStats) {
					p4m.logger.Debugf("publishCumulative")
				}
				if !p4m.historical {
					metricsChan <- p4m.getCumulativeMetrics()
				}
			case cmd, ok := <-cmdsInChan:
				if ok {
					if p4m.logger.Level > logrus.DebugLevel && p4dlog.FlagSet(p4m.debug, p4dlog.DebugCommands) {
						p4m.logger.Tracef("Publishing cmd: %s", cmd.String())
					}
					p4m.cmdsProcessed++
					p4m.publishEvent(cmd)
					if needCmdChan {
						cmdsOutChan <- cmd
					}
				} else {
					p4m.logger.Debugf("FP Cmd closed")
					metricsChan <- p4m.getCumulativeMetrics()
					return
				}
			case line, ok := <-linesInChan:
				if ok {
					if p4m.logger.Level > logrus.DebugLevel && p4dlog.FlagSet(p4m.debug, p4dlog.DebugLines) {
						p4m.logger.Tracef("Line: %s", line)
					}
					p4m.linesRead++
					fpLinesChan <- line
					if p4m.historical && p4m.historicalUpdateRequired(line) {
						metricsChan <- p4m.getCumulativeMetrics()
					}
				} else {
					if fpLinesChan != nil {
						p4m.logger.Debugf("Lines closed")
						close(fpLinesChan)
						fpLinesChan = nil
					}
				}
			}
		}
	}()

	return cmdsOutChan, metricsChan
}
