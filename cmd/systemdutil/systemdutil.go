package systemdutil

import (
	"fmt"
	"log"
	"net"
	"net/rpc/jsonrpc"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aporeto-inc/trireme/constants"
	"github.com/aporeto-inc/trireme/monitor"
	"github.com/aporeto-inc/trireme/monitor/rpcmonitor"
	"github.com/aporeto-inc/trireme/policy"
)

const (
	maxRetries       = 20
	remoteMethodCall = "Server.HandleEvent"
)

var stderrlogger *log.Logger

func init() {
	stderrlogger = log.New(os.Stderr, "", 0)
}

// ExecuteCommandFromArguments processes the command from the arguments
func ExecuteCommandFromArguments(arguments map[string]interface{}) error {

	p := NewRequestProcessor()

	c, err := p.ParseCommand(arguments)
	if err != nil {
		return err
	}

	return p.ExecuteRequest(c)
}

// RequestType is the type of the request
type RequestType int

const (
	// CreateRequest requests a create event
	CreateRequest RequestType = iota
	// DeleteCgroupRequest requests deletion based on the cgroup - issued by the kernel
	DeleteCgroupRequest
	// DeleteServiceRequest requests deletion by the service ID
	DeleteServiceRequest
)

// CLIRequest captures all CLI parameters
type CLIRequest struct {
	// Request is the type of the request
	Request RequestType
	// Cgroup is only provided for delete cgroup requests
	Cgroup string
	// Executable is the path to the executable
	Executable string
	// Parameters are the parameters of the executable
	Parameters []string
	// Labels are the user labels attached to the request
	Labels []string
	// ServiceName is a user defined service name
	ServiceName string
	// ServiceID is the ID of the service
	ServiceID string
	// Services are the user defined services (protocol, port)
	Services []policy.Service
	// HostPolicy indicates that this is a host policy
	HostPolicy bool
	// NetworkOnly indicates that the request is only for traffic coming from the network
	NetworkOnly bool
}

// RequestProcessor is an instance of the processor
type RequestProcessor struct {
	LinuxPath string
	HostPath  string
}

// NewRequestProcessor creates a default request processor
func NewRequestProcessor() *RequestProcessor {
	return &RequestProcessor{
		LinuxPath: "/var/run/trireme/linux",
		HostPath:  "/var/run/trireme/host",
	}
}

// NewCustomRequestProcessor creates a new request processor
func NewCustomRequestProcessor(linux, host string) *RequestProcessor {
	r := NewRequestProcessor()

	if linux != "" {
		r.LinuxPath = linux
	}

	if host != "" {
		r.HostPath = host
	}

	return r
}

// Generic command line arguments
// Assumes a command like that:
// usage = `Trireme Client Command
//
// Usage: enforcerd -h | --help
// 		 trireme -v | --version
// 		 trireme run
// 			[--service-name=<sname>]
// 			[[--ports=<ports>]...]
// 			[[--label=<keyvalue>]...]
// 			[--networkonly]
// 			[--hostpolicy]
// 			[<command> [--] [<params>...]]
// 		 trireme rm
//      [--service-id=<id>]
//      [--service-name=<sname>]
// 		 trireme <cgroup>
//
// Run Client Options:
// 	--service-name=<sname>              Service name for the executed command [default ].
// 	--ports=<ports>                     Ports that the executed service is listening to [default ].
// 	--label=<keyvalue>                  Label (key/value pair) attached to the service [default ].
// 	--networkonly                       Control traffic from the network only and not from applications [default false].
// 	--hostpolicy                        Default control of the base namespace [default false].
//
// `

// ParseCommand parses a command based on the above specification
// This is a helper function for CLIs like in Trireme Example.
// Proper use is through the CLIRequest structure
func (r *RequestProcessor) ParseCommand(arguments map[string]interface{}) (*CLIRequest, error) {

	c := &CLIRequest{}

	// First parse a command that only provides the cgroup
	// The kernel will only send us a command with one argument
	if value, ok := arguments["<cgroup>"]; ok && value != nil {
		c.Cgroup = value.(string)
		c.Request = DeleteCgroupRequest
		return c, nil
	}

	if value, ok := arguments["--service-id"]; ok && value != nil {
		c.ServiceID = value.(string)
	}

	if value, ok := arguments["--service-name"]; ok && value != nil {
		c.ServiceName = value.(string)
	}

	// If the command is remove use hostpolicy and service-id
	if arguments["rm"].(bool) {
		c.Request = DeleteServiceRequest
		return c, nil
	}

	// Process the rest of the arguments of the create command
	if value, ok := arguments["run"]; !ok || value == nil {
		return nil, fmt.Errorf("No valid command provided")
	}

	// This is a create request - proceed
	c.Request = CreateRequest

	if value, ok := arguments["<command>"]; ok && value != nil {
		c.Executable = value.(string)
	}

	if value, ok := arguments["--label"]; ok && value != nil {
		c.Labels = value.([]string)
	}

	if value, ok := arguments["<params>"]; ok && value != nil {
		c.Parameters = value.([]string)
	}

	if value, ok := arguments["--ports"]; ok && value != nil {
		services, err := ParseServices(value.([]string))
		if err != nil {
			return nil, err
		}
		c.Services = services
	}

	if value, ok := arguments["--networkonly"]; ok && value != nil {
		c.NetworkOnly = value.(bool)
	}

	if value, ok := arguments["--hostpolicy"]; ok && value != nil {
		c.HostPolicy = value.(bool)
	}

	return c, nil
}

// CreateAndRun creates a processing unit
func (r *RequestProcessor) CreateAndRun(c *CLIRequest) error {
	var err error

	// If its not hostPolicy and the command doesn't exist we return an error
	if !c.HostPolicy {
		if c.Executable == "" {
			return fmt.Errorf("Command must be provided")
		}
		if !path.IsAbs(c.Executable) {
			c.Executable, err = exec.LookPath(c.Executable)
			if err != nil {
				return err
			}
		}
		if c.ServiceName == "" {
			c.ServiceName = c.Executable
		}
	}

	// Determine the right RPC address. If we are not root, Root RPC will reject.
	rpcAdress := rpcmonitor.DefaultRPCAddress
	if c.HostPolicy {
		rpcAdress = rpcmonitor.DefaultRootRPCAddress
	}

	//This is added since the release_notification comes in this format
	//Easier to massage it while creation rather than change at the receiving end depending on event
	request := &rpcmonitor.EventInfo{
		PUType:             constants.LinuxProcessPU,
		Name:               c.ServiceName,
		Tags:               c.Labels,
		PID:                strconv.Itoa(os.Getpid()),
		EventType:          "start",
		Services:           c.Services,
		NetworkOnlyTraffic: c.NetworkOnly,
		HostService:        c.HostPolicy,
	}

	if err := sendRPC(rpcAdress, request); err != nil {
		return err
	}

	if c.HostPolicy {
		return nil
	}

	return syscall.Exec(c.Executable, append([]string{c.Executable}, c.Parameters...), os.Environ())
}

// Delete will issue a delete command
func (r *RequestProcessor) Delete(c *CLIRequest) error {
	if c.Cgroup == "" && c.ServiceName == "" && c.ServiceID == "" {
		return fmt.Errorf("cgroup, service ID, or service name must be defined")
	}

	rpcAdress := rpcmonitor.DefaultRPCAddress
	linuxPath := r.LinuxPath
	puid := c.ServiceID
	host := false
	if c.ServiceName != "" {
		rpcAdress = rpcmonitor.DefaultRootRPCAddress
		linuxPath = r.HostPath
		puid = c.ServiceName
		host = true
	}

	request := &rpcmonitor.EventInfo{
		PUType:      constants.LinuxProcessPU,
		PUID:        puid,
		Cgroup:      c.Cgroup,
		EventType:   monitor.EventStop,
		HostService: host,
	}
	// Handle the special case with the User ID monitor and deletes
	if c.Cgroup != "" {
		parts := strings.Split(c.Cgroup, "/")
		if len(parts) != 3 {
			return fmt.Errorf("Invalid Cgroup")
		}

		if !c.HostPolicy {
			if _, ferr := os.Stat(filepath.Join(linuxPath, parts[2])); os.IsNotExist(ferr) {
				request.PUType = constants.UIDLoginPU
			}
		}
	}

	// Send Stop request
	if err := sendRPC(rpcAdress, request); err != nil {
		return err
	}

	// Send destroy request
	request.EventType = monitor.EventDestroy

	return sendRPC(rpcAdress, request)
}

// ExecuteRequest executes the command with an RPC request
func (r *RequestProcessor) ExecuteRequest(c *CLIRequest) error {

	switch c.Request {
	case CreateRequest:
		return r.CreateAndRun(c)
	case DeleteCgroupRequest, DeleteServiceRequest:
		return r.Delete(c)
	default:
		return fmt.Errorf("Unknown command")
	}
}

// sendRPC sends an RPC request to the provided address
func sendRPC(address string, request *rpcmonitor.EventInfo) error {
	// Make RPC call and only retry if the resource is temporarily unavailable
	numRetries := 0
	client, err := net.Dial("unix", address)
	for err != nil {
		numRetries++
		nerr, ok := err.(*net.OpError)

		if numRetries >= maxRetries || !(ok && nerr.Err == syscall.EAGAIN) {
			err = fmt.Errorf("Cannot connect to policy process %s", err)
			stderrlogger.Print(err)
			return err
		}

		time.Sleep(5 * time.Millisecond)
		client, err = net.Dial("unix", address)
	}

	response := &rpcmonitor.RPCResponse{}

	rpcClient := jsonrpc.NewClient(client)

	err = rpcClient.Call(remoteMethodCall, request, response)
	if err != nil {
		err = fmt.Errorf("Policy Server call failed %s", err.Error())
		stderrlogger.Print(err)
		return err
	}

	if len(response.Error) > 0 {
		err = fmt.Errorf("Your policy does not allow you to run this command")
		stderrlogger.Print(err)
		return err
	}

	return nil
}

// ParseServices parses strings with the services and returns them in an
// validated slice
func ParseServices(ports []string) ([]policy.Service, error) {
	// If no ports are provided, we add the default 0 port
	if len(ports) == 0 {
		ports = append(ports, "0")
	}

	// Parse the ports and create the services. Cleanup any bad ports
	services := []policy.Service{}
	for _, p := range ports {
		intPort, ierr := strconv.Atoi(p)
		if ierr != nil || intPort > 0xFFFF || intPort < 0 {
			return nil, fmt.Errorf("Invalid services")
		}

		// TODO: Assumes only TCP here until we add UDP support
		services = append(services, policy.Service{
			Protocol: uint8(6),
			Port:     uint16(intPort),
		})
	}

	return services, nil
}
