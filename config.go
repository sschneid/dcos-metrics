package main

import (
	"flag"
	"fmt"
	"io/ioutil"

	"github.com/dcos/dcos-go/dcos"
	"github.com/dcos/dcos-go/dcos/nodeutil"
	"github.com/dcos/dcos-metrics/collector/mesos_agent"
	"github.com/dcos/dcos-metrics/collector/node"
	httpHelpers "github.com/dcos/dcos-metrics/http_helpers"
	httpProducer "github.com/dcos/dcos-metrics/producers/http"

	log "github.com/Sirupsen/logrus"
	yaml "gopkg.in/yaml.v2"
)

var (
	// VERSION set by $(git describe --always)
	// Set by scripts/build.sh, executed by `make build`
	VERSION = "unset"
	// REVISION set by $(git rev-parse --shore HEAD)
	// Set by scripts/build.sh, executed by `make build`
	REVISION = "unset"
)

// Config defines the top-level configuration options for the dcos-metrics-collector project.
// It is (currently) broken up into two main sections: collectors and producers.
type Config struct {
	// Config from the service config file
	Collector         CollectorConfig `yaml:"collector"`
	Producers         ProducersConfig `yaml:"producers"`
	IAMConfigPath     string          `yaml:"iam_config_path"`
	CACertificatePath string          `yaml:"ca_certificate_path"`
	VersionFlag       bool

	// Generated by dcos.NodeInfo{}
	MesosID   string
	IPAddress string
	ClusterID string

	// Flag configuration
	DCOSRole   string
	ConfigPath string
	LogLevel   string
}

// CollectorConfig contains configuration options relevant to the "collector"
// portion of this project. That is, the code responsible for querying Mesos,
// et. al to gather metrics and send them to a "producer".
type CollectorConfig struct {
	HTTPProfiler bool                             `yaml:"http_profiler"`
	Node         *node.NodeCollector              `yaml:"node,omitempty"`
	MesosAgent   *mesos_agent.MesosAgentCollector `yaml:"mesos_agent,omitempty"`
}

// ProducersConfig contains references to other structs that provide individual producer configs.
// The configuration for all producers is then located in their corresponding packages.
//
// For example: Config.Producers.KafkaProducerConfig references kafkaProducer.Config. This struct
// contains an optional Kafka configuration. This configuration is available in the source file
// 'producers/kafka/kafka.go'. It is then the responsibility of the individual producers to
// validate the configuration the user has provided and panic if necessary.
type ProducersConfig struct {
	HTTPProducerConfig httpProducer.Config `yaml:"http,omitempty"`
	//KafkaProducerConfig  kafkaProducer.Config  `yaml:"kafka,omitempty"`
	//StatsdProducerConfig statsdProducer.Config `yaml:"statsd,omitempty"`
}

func (c *Config) setFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.ConfigPath, "config", c.ConfigPath, "The path to the config file.")
	fs.StringVar(&c.LogLevel, "loglevel", c.LogLevel, "Logging level (default: info). Must be one of: debug, info, warn, error, fatal, panic.")
	fs.StringVar(&c.DCOSRole, "role", c.DCOSRole, "The DC/OS role this instance runs on.")
	fs.BoolVar(&c.VersionFlag, "version", c.VersionFlag, "Print version and revsion then exit")
}

func (c *Config) loadConfig() error {
	fileByte, err := ioutil.ReadFile(c.ConfigPath)
	if err != nil {
		return err
	}

	if err = yaml.Unmarshal(fileByte, &c); err != nil {
		return err
	}

	return nil
}

func (c *Config) getNodeInfo() error {
	log.Debug("Getting node info")
	client, err := httpHelpers.NewMetricsClient(c.CACertificatePath, c.IAMConfigPath)
	if err != nil {
		return err
	}

	// Get NodeInfo
	var stateURL = "http://leader.mesos:5050/state"
	if len(c.IAMConfigPath) > 0 {
		stateURL = "https://leader.mesos:5050/state"
	}
	nodeInfo, err := nodeutil.NewNodeInfo(client, nodeutil.OptionMesosStateURL(stateURL))
	if err != nil {
		log.Errorf("Error getting NodeInfo{}: err")
	}

	ip, err := nodeInfo.DetectIP()
	if err != nil {
		log.Error(err)
	}
	c.Collector.MesosAgent.NodeInfo.IPAddress = ip.String()
	c.Collector.Node.NodeInfo.IPAddress = ip.String()

	mid, err := nodeInfo.MesosID(nil)
	if err != nil {
		log.Error(err)
	}
	c.Collector.MesosAgent.NodeInfo.MesosID = mid
	c.Collector.Node.NodeInfo.MesosID = mid

	if c.DCOSRole == dcos.RoleMaster {
		c.Collector.Node.NodeInfo.ClusterID, err = nodeInfo.ClusterID()
		if err != nil {
			return err
		}
	}

	return nil
}

// newConfig establishes our default, base configuration.
func newConfig() Config {
	return Config{
		Collector: CollectorConfig{
			HTTPProfiler: true,
			MesosAgent: &mesos_agent.MesosAgentCollector{
				PollPeriod: 15,
				Port:       5051,
			},
			Node: &node.NodeCollector{
				PollPeriod: 15,
			},
		},
		Producers: ProducersConfig{
			HTTPProducerConfig: httpProducer.Config{
				Port: 8000,
			},
		},
		ConfigPath: "dcos-metrics-config.yaml",
		LogLevel:   "info",
	}
}

// getNewConfig loads the configuration and sets precedence of configuration values.
// For example: command line flags override values provided in the config file.
func getNewConfig(args []string) (Config, error) {
	c := newConfig()
	thisFlagSet := flag.NewFlagSet("", flag.ExitOnError)
	c.setFlags(thisFlagSet)
	// Override default config with CLI flags if any
	if err := thisFlagSet.Parse(args); err != nil {
		fmt.Println("Errors encountered parsing flags.")
		return c, err
	}

	if err := c.loadConfig(); err != nil {
		return c, err
	}

	// Note: .getNodeInfo() is last so we are sure we have all the
	// configuration we need from flags and config file to make
	// this run correctly.
	if err := c.getNodeInfo(); err != nil {
		return c, err
	}

	// Set the client for the collector to reuse in GET operations
	// to local state and other HTTP sessions
	collectorClient, err := httpHelpers.NewMetricsClient(c.CACertificatePath, c.IAMConfigPath)
	if err != nil {
		return c, err
	}

	c.Collector.MesosAgent.HTTPClient = collectorClient

	return c, nil
}
