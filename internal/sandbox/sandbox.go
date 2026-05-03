package sandbox

import (
	"context"
	"fmt"
	"io"
	"time"
)

// State represents the lifecycle of a sandbox.
type State int

const (
	StatePending      State = iota // Created, not yet provisioned
	StateProvisioning              // Container being created + workspace copied
	StateReady                     // Container running, workspace initialized
	StateRunning                   // Agent executing inside container
	StateStopped                   // Agent finished, container still exists
	StateExtracting                // Extracting changed files from container
	StateDestroyed                 // Container and volume removed
)

func (s State) String() string {
	switch s {
	case StatePending:
		return "pending"
	case StateProvisioning:
		return "provisioning"
	case StateReady:
		return "ready"
	case StateRunning:
		return "running"
	case StateStopped:
		return "stopped"
	case StateExtracting:
		return "extracting"
	case StateDestroyed:
		return "destroyed"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// Terminal returns true if the state is a final state.
func (s State) Terminal() bool {
	return s == StateDestroyed
}

// FileOp classifies what happened to a file inside the sandbox.
type FileOp string

const (
	FileAdded    FileOp = "added"
	FileModified FileOp = "modified"
	FileDeleted  FileOp = "deleted"
)

// ChangedFile describes a single file that was modified inside the sandbox.
type ChangedFile struct {
	Path         string // relative to workspace root
	Op           FileOp
	Size         int64
	IsExecutable bool
	ContentHash  string // SHA-256 hex
}

// MountConfig describes a host path to mount read-only inside the container.
type MountConfig struct {
	HostPath      string `yaml:"host"`
	ContainerPath string `yaml:"container"`
}

// MCPConfig configures MCP server access from within the sandbox.
type MCPConfig struct {
	SocketPath string `yaml:"socket_path"` // Docker MCP gateway socket path on host
}

// Config configures a sandbox instance.
type Config struct {
	Image              string        `yaml:"image"`
	Memory             string        `yaml:"memory"`       // e.g. "4g"
	CPUs               float64       `yaml:"cpus"`         // e.g. 2.0
	PidsLimit          int64         `yaml:"pids_limit"`   // max PIDs in container
	MaxLifetime        time.Duration `yaml:"max_lifetime"` // hard kill after this duration
	Network            string        `yaml:"network"`      // Docker network mode (e.g. "host", "bridge")
	ReadOnlyMounts     []MountConfig `yaml:"read_only_mounts"`
	BindMounts         []MountConfig `yaml:"bind_mounts"`         // read-write bind mounts
	AllowedExecutables []string      `yaml:"allowed_executables"` // glob patterns for allowed executable files
	MCP                MCPConfig     `yaml:"mcp"`
}

// DefaultConfig returns a sandbox config with sane defaults.
func DefaultConfig() Config {
	return Config{
		Image:       "orqestra-sandbox:latest",
		Memory:      "4g",
		CPUs:        2,
		PidsLimit:   256,
		MaxLifetime: 50 * time.Minute,
		MCP:         MCPConfig{SocketPath: "/run/host-services/docker.proxy.sock"},
	}
}

// Sandbox is the interface for isolated agent execution environments.
type Sandbox interface {
	// ID returns the unique identifier of this sandbox.
	ID() string

	// State returns the current lifecycle state.
	State() State

	// Provision creates and initializes the sandbox (container + workspace copy).
	// Transitions: Pending → Provisioning → Ready.
	Provision(ctx context.Context) error

	// Exec runs a command inside the sandbox, streaming output to stdout.
	// Transitions: Ready → Running → Stopped.
	Exec(ctx context.Context, command []string, env []string, stdout io.Writer) (int, error)

	// ExtractChanges diffs the sandbox workspace against the original and returns changed files.
	// Transitions: Stopped → Extracting → Stopped.
	ExtractChanges(ctx context.Context) ([]ChangedFile, error)

	// CopyOut copies a changed file from the sandbox to a host destination path.
	CopyOut(ctx context.Context, sandboxPath, hostPath string) error

	// Destroy tears down the sandbox and cleans up all resources.
	// Can be called from any state. Transitions: * → Destroyed.
	Destroy(ctx context.Context) error

	// Info returns metadata about the sandbox for display.
	Info() Info
}

// Info contains display metadata for a sandbox.
type Info struct {
	ID          string
	ContainerID string // short container hash for display
	State       State
	CreatedAt   time.Time
	Image       string
}
