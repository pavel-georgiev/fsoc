package melt

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/apex/log"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"

	"github.com/cisco-open/fsoc/config"
	"github.com/cisco-open/fsoc/output"
	"github.com/cisco-open/fsoc/platform/melt"
)

var meltPushCmd = &cobra.Command{
	Use:   "push [DATAFILE]",
	Short: "Generates OTLP telemetry based on fsoc telemetry data model .yaml",
	Long: `
This command generates OTLP payload based on a fsoc telemetry data models and sends the data to the FSO Platform Ingestion services.

To properly use the command you will need to create a fsoc profile using an agent principal yaml:
fsoc config set --profile=<agent-principal-profile> auth=agent-principal secret-file=<agent-principal.yaml>

Then you will use the agent principal profile as part of the command:
fsoc melt push <fsocdatamodel>.yaml --profile <agent-principal-profile>

Or use input from STDIN:
cat <fsocdatamodel>.yaml | fsoc melt push --profile <agent-principal-profile>
`,
	TraverseChildren: true,
	Args:             cobra.MaximumNArgs(1),
	Run:              meltSend,
}

func init() {
	meltPushCmd.Flags().Bool("dump", false, "Display MELT data protobuf payloads")
	meltCmd.AddCommand(meltPushCmd)
}

func meltSend(cmd *cobra.Command, args []string) {
	ctx := config.GetCurrentContext()
	if ctx.AuthMethod != config.AuthMethodAgentPrincipal {
		_ = cmd.Help()
		log.Fatalf("This command requires a profile with \"agent-principal\" auth method, found %q instead", ctx.AuthMethod)
	}
	// Make this tolerate empty arg list, in which case it should use stdin
	var dataFileName string
	if len(args) > 0 {
		dataFileName = args[0]
	}
	sendDataFromFile(cmd, dataFileName)
}

func sendDataFromFile(cmd *cobra.Command, dataFileName string) {
	fsoData, err := loadDataFile(dataFileName)
	if err != nil {
		log.Fatalf("Can't open data file %q: %v", dataFileName, err)
	}

	for _, entity := range fsoData.Melt {
		if _, ok := entity.Attributes["telemetry.sdk.name"]; ok {
			log.Info("telemetry.sdk.name already set, skipping...")
		} else {
			entity.SetAttribute("telemetry.sdk.name", "fsoc-melt")
		}
		for _, m := range entity.Metrics {
			et := time.Now()
			if len(m.DataPoints) == 0 {
				for i := 1; i < 6; i++ {
					st := et.Add(time.Minute * -1)

					// m.AddDataPoint(st.UnixNano(), et.UnixNano(), rand.Float64()*50)

					// 2023-10-04, Wayne Brown
					// Adding in code to allow for min / max thresholds.
					// Four cases to consider: min / max both set, min set, max set, and neither are set
					dp := rand.Float64() * 50

					if m.Min != "" && m.Max != "" {
						dpmin, min_err := strconv.ParseFloat(m.Min, 64)
						dpmax, max_err := strconv.ParseFloat(m.Max, 64)

						if min_err != nil || max_err != nil {
							if min_err != nil {
								log.Warnf("Could not parse min value for %q.", entity.TypeName)
							}
							if max_err != nil {
								log.Warnf("Could not parse max value for %q.", entity.TypeName)
							}
						} else {

							// If max is less than min, swap them
							if dpmin > dpmax {
								dpmin, dpmax = dpmax, dpmin
							}

							dp = (rand.Float64() * (dpmax - dpmin)) + dpmin
						}

					} else if m.Max != "" {
						dpmax, max_err := strconv.ParseFloat(m.Max, 64)

						if max_err != nil {
							log.Warnf("Could not parse max value for %q.", entity.TypeName)
						} else {
							dp = rand.Float64() * dpmax
						}
					} else if m.Min != "" {
						dpmin, min_err := strconv.ParseFloat(m.Min, 64)

						if min_err != nil {
							log.Warnf("Could not parse min value for %q.", entity.TypeName)
						} else {
							// For setting a floor value, taking the approach of starting at the minimum
							// and using
							dp = dpmin + (rand.Float64() * 50)
						}
					}

					m.AddDataPoint(st.UnixNano(), et.UnixNano(), dp)
					et = st
				}
			}
		}
		for _, l := range entity.Logs {
			if l.Timestamp == 0 {
				l.Timestamp = time.Now().UnixNano()
			}
		}
	}

	exportMeltStraight(cmd, fsoData)
}

func exportMeltStraight(cmd *cobra.Command, fsoData *melt.FsocData) {
	exportMelt(cmd, *fsoData)
}

func exportMelt(cmd *cobra.Command, fsoData melt.FsocData) {
	// prepare a dump function with closure
	dump, _ := cmd.Flags().GetBool("dump")
	var dumpFunc func(s string)
	if dump {
		dumpFunc = func(s string) {
			output.PrintCmdStatus(cmd, s)
		}
	}

	// invoke the exporter
	exp := &melt.Exporter{DumpFunc: dumpFunc}

	if !dump {
		output.PrintCmdStatus(cmd, "Generating new MELT telemetry\n")
	}

	printSection(cmd, "Metrics", dump)
	err := exp.ExportMetrics(fsoData.Melt)
	if err != nil {
		log.Fatalf("Error exporting metrics: %s", err)
	}

	printSection(cmd, "Logs", dump)
	err = exp.ExportLogs(fsoData.Melt)
	if err != nil {
		log.Fatalf("Error exporting logs: %s", err)
	}

	printSection(cmd, "Spans", dump)
	err = exp.ExportSpans(fsoData.Melt)
	if err != nil {
		log.Fatalf("Error exporting spans: %s", err)
	}

	if !dump {
		output.PrintCmdStatus(cmd, "\nMELT data sent (see log for traceresponse ID)\n")
	}
}

func loadDataFile(fileName string) (*melt.FsocData, error) {
	var fsoData *melt.FsocData
	var dataFile *os.File

	if fileName == "" {
		output.PrintCmdStatus(nil, "Reading from STDIN\n")
		dataFile = os.Stdin
	} else {
		dataFile, err := os.Open(fileName)
		if err != nil {
			log.Fatalf("Can't open the file named %q: %v", fileName, err)
		}
		defer dataFile.Close()
	}

	dataBytes, err := io.ReadAll(dataFile)
	if err != nil {
		log.Fatalf("Can't read the file %q: %v", fileName, err)
	}

	err = yaml.Unmarshal(dataBytes, &fsoData)
	if err != nil {
		log.Fatalf("Failed to parse fsoc telemetry model file: %v", err)
	}

	return fsoData, nil
}

func printSection(cmd *cobra.Command, section string, dump bool) {
	var s string
	if dump {
		// format the section as a comment, separate from dump
		s = fmt.Sprintf("\n# %s\n", section)
	} else {
		// format the section name as progress
		s = fmt.Sprintf("  Sending %s...\n", section)
	}
	output.PrintCmdStatus(cmd, s)
}
