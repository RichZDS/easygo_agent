package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const ownerLabel = "ai.easygo.workshop.owner"
const managedLabel = "ai.easygo.workshop.managed"
const rootLabel = "ai.easygo.workshop.root"
const runtimeWorkspace = "/workspace"
const runtimeRelay = "/run/easygo-relay"
const relayDirectory = "relays"
const relayRunPrefix = "run-"
const relaySocketName = "model.sock"
const relaySocketPathLimit = 107

// Go 1.25 os.MkdirTemp appends nextRandom's decimal uint32: at most 10 digits.
// Share directory/name constants with Run so validation covers the actual path.
const relayMaxRandomSuffix = "4294967295"

// RelaySocketPathError is safe to surface at startup: it contains lengths only,
// never operator paths or credentials from other configuration errors.
type RelaySocketPathError struct{ ActualBytes int }

func (e *RelaySocketPathError) Error() string {
	return fmt.Sprintf("relay socket path exceeds %d-byte limit: worst-case length %d bytes; shorten workshop root", relaySocketPathLimit, e.ActualBytes)
}
func (e *RelaySocketPathError) Unwrap() error { return ErrInvalid }

func validateRelaySocketPath(root string) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	length := len(filepath.Join(absolute, relayDirectory, relayRunPrefix+relayMaxRandomSuffix, relaySocketName))
	if length > relaySocketPathLimit {
		return &RelaySocketPathError{ActualBytes: length}
	}
	return nil
}

// SandboxConfig is operator configuration, never accepted from task callers.
// HostRoot is the daemon-visible path of Config.Root, not a parent directory.
// Empty Mode preserves the legacy unmanaged host API; managed deployments set docker.
type SandboxConfig struct {
	Mode           string `json:"mode"`
	DockerBinary   string `json:"docker_binary,omitempty"`
	Endpoint       string `json:"endpoint,omitempty"`
	Image          string `json:"image,omitempty"`
	Owner          string `json:"owner,omitempty"`
	HostRoot       string `json:"host_root,omitempty"`
	MemoryBytes    int64  `json:"memory_bytes,omitempty"`
	NanoCPUs       int64  `json:"nano_cpus,omitempty"`
	PIDsLimit      int64  `json:"pids_limit,omitempty"`
	TmpfsBytes     int64  `json:"tmpfs_bytes,omitempty"`
	DiskQuotaBytes int64  `json:"disk_quota_bytes,omitempty"`
	DiskQuotaFiles int64  `json:"disk_quota_files,omitempty"`
	DiskPollMS     int    `json:"disk_poll_ms,omitempty"`
}

type dockerCommand func(context.Context, io.Reader, io.Writer, io.Writer, ...string) error

type DockerRunner struct {
	cfg               SandboxConfig
	root              string
	gateway           *ModelGateway
	maxOutput         int
	command           dockerCommand
	mu                sync.Mutex
	cleanupFailure    error
	initialized       bool
	finalQuotaTimeout time.Duration // test seam; zero keeps the 30-second final scan budget
}

var ownerPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && !strings.ContainsAny(path, ",\n\r\x00")
}

// Reject symlink components, including ancestors outside the configured root.
// Workspaces are not concurrently mounted by this service during preparation.
func noSymlinks(path string) error {
	if !cleanAbsolute(path) {
		return errors.New("path must be clean and absolute")
	}
	for p := path; p != "/"; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("directory path contains symlink or non-directory")
		}
	}
	return nil
}

// Create each component only after its parent is verified; MkdirAll alone can
// follow an existing symlink and mutate a directory outside the task root.
func mkdirNoSymlinks(path string, mode os.FileMode) error {
	if !cleanAbsolute(path) {
		return errors.New("path must be clean and absolute")
	}
	parent := filepath.Dir(path)
	if parent != "/" {
		if err := mkdirNoSymlinks(parent, mode); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, mode); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("directory path contains symlink or non-directory")
	}
	return nil
}

func normalizeSandbox(c SandboxConfig) (SandboxConfig, error) {
	if c.Mode != "docker" || !cleanAbsolute(c.HostRoot) || !ownerPattern.MatchString(c.Owner) || c.Image == "" || strings.HasPrefix(c.Image, "-") || strings.ContainsAny(c.Image, " \t\r\n\x00") {
		return c, fmt.Errorf("%w: docker requires image, owner and absolute host_root", ErrInvalid)
	}
	if !strings.HasPrefix(c.Endpoint, "unix://") || !cleanAbsolute(strings.TrimPrefix(c.Endpoint, "unix://")) {
		return c, fmt.Errorf("%w: Docker endpoint must be an explicit Unix socket", ErrInvalid)
	}
	if c.MemoryBytes == 0 {
		c.MemoryBytes = 1024 << 20
	}
	if c.NanoCPUs == 0 {
		c.NanoCPUs = 1_000_000_000
	}
	if c.PIDsLimit == 0 {
		c.PIDsLimit = 128
	}
	if c.TmpfsBytes == 0 {
		c.TmpfsBytes = 64 << 20
	}
	if c.MemoryBytes < 64<<20 || c.MemoryBytes > 64<<30 || c.NanoCPUs < 100_000_000 || c.NanoCPUs > 32_000_000_000 || c.PIDsLimit < 16 || c.PIDsLimit > 4096 || c.TmpfsBytes < 1<<20 || c.TmpfsBytes > 1<<30 {
		return c, fmt.Errorf("%w: invalid Docker resource bounds", ErrInvalid)
	}
	if c.DiskQuotaBytes == 0 {
		c.DiskQuotaBytes = 2 << 30
	}
	if c.DiskQuotaFiles == 0 {
		c.DiskQuotaFiles = 200000
	}
	if c.DiskPollMS == 0 {
		c.DiskPollMS = 2000
	}
	if c.DiskQuotaBytes < 16<<20 || c.DiskQuotaBytes > 1<<40 || c.DiskQuotaFiles < 1000 || c.DiskQuotaFiles > 10000000 || c.DiskPollMS < 200 || c.DiskPollMS > 60000 {
		return c, fmt.Errorf("%w: invalid disk quota bounds", ErrInvalid)
	}
	return c, nil
}

func NewDockerRunner(cfg Config) (*DockerRunner, error) {
	if err := validateRelaySocketPath(cfg.Root); err != nil {
		return nil, err
	}
	c, err := normalizeSandbox(cfg.Sandbox)
	if err != nil {
		return nil, err
	}
	if err = noSymlinks(cfg.Root); err != nil {
		return nil, err
	}
	max := cfg.MaxOutputBytes
	if max == 0 {
		max = defaultOutputLimit
	}
	if max < 1024 || max > 64<<20 {
		return nil, ErrInvalid
	}
	binary := c.DockerBinary
	if binary == "" {
		binary = "docker"
	}
	binary, err = exec.LookPath(binary)
	if err != nil {
		return nil, errors.New("Docker executable unavailable")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, err
	}
	r := &DockerRunner{cfg: c, root: cfg.Root, gateway: cfg.ModelGateway, maxOutput: max}
	r.command = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
		cmd := exec.CommandContext(ctx, binary, append([]string{"--host", c.Endpoint}, args...)...)
		// Never inherit DOCKER_HOST, contexts, auth, plugin paths or service secrets.
		cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/nonexistent", "DOCKER_CONFIG=/nonexistent"}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
		cmd.WaitDelay = time.Second
		if err := configureProcess(cmd); err != nil {
			return err
		}
		err := cmd.Run()
		killProcessGroup(cmd)
		return err
	}
	return r, nil
}

func (r *DockerRunner) output(ctx context.Context, args ...string) (string, error) {
	b := &boundedBuffer{limit: 1 << 20}
	diagnostic := &boundedBuffer{limit: 4096}
	if err := r.command(ctx, nil, b, diagnostic, args...); err != nil {
		return "", errors.New("Docker operation failed: " + args[0])
	}
	if b.overflow {
		return "", errors.New("Docker response exceeds limit")
	}
	return strings.TrimSpace(b.buf.String()), nil
}

func (r *DockerRunner) labels() map[string]string {
	return map[string]string{managedLabel: "true", ownerLabel: r.cfg.Owner, rootLabel: r.cfg.HostRoot}
}

// hostPath performs lexical mapping only after validating the local source.
// Initialize attests the mapping through the daemon before any task is admitted.
func (r *DockerRunner) hostPath(local string) (string, error) {
	if err := noSymlinks(local); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(r.root, local)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", errors.New("bind source escapes workshop root")
	}
	return filepath.Join(r.cfg.HostRoot, rel), nil
}

func (r *DockerRunner) containerOptions(name, workspace, relay string) []string {
	args := []string{"create", "--name", name, "--pull", "never", "--interactive", "--user", "1000:1000", "--workdir", runtimeWorkspace, "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--pids-limit", strconv.FormatInt(r.cfg.PIDsLimit, 10), "--memory", strconv.FormatInt(r.cfg.MemoryBytes, 10), "--memory-swap", strconv.FormatInt(r.cfg.MemoryBytes, 10), "--cpus", strconv.FormatFloat(float64(r.cfg.NanoCPUs)/1e9, 'f', 9, 64), "--ipc", "private", "--shm-size", "8388608", "--log-driver", "none", "--tmpfs", fmt.Sprintf("/tmp:rw,nosuid,nodev,noexec,size=%d,mode=1777", r.cfg.TmpfsBytes), "--entrypoint", "/usr/local/bin/task-shim"}
	args = append(args, "--ulimit", fmt.Sprintf("fsize=%d:%d", r.cfg.DiskQuotaBytes, r.cfg.DiskQuotaBytes))
	labels := r.labels()
	keys := []string{managedLabel, ownerLabel, rootLabel}
	for _, k := range keys {
		args = append(args, "--label", k+"="+labels[k])
	}
	args = append(args, "--mount", "type=bind,src="+workspace+",dst="+runtimeWorkspace+",bind-propagation=rprivate")
	if relay != "" {
		args = append(args, "--mount", "type=bind,src="+relay+",dst="+runtimeRelay+",readonly,bind-propagation=rprivate")
	}
	return args
}

// A read-only workflow has an OS-enforced read-only workspace. Native state
// remains writable only under its explicit task-private HOME submount.
func (r *DockerRunner) taskContainerOptions(name, workspace, relay, home, policy string) []string {
	args := r.containerOptions(name, workspace, relay)
	if policy == "read-only" {
		for i, arg := range args {
			if arg == "--mount" && strings.Contains(args[i+1], ",dst="+runtimeWorkspace+",") {
				args[i+1] += ",readonly"
			}
		}
		args = append(args, "--mount", "type=bind,src="+home+",dst="+runtimeWorkspace+"/.workshop-home,bind-propagation=rprivate")
	}
	return args
}

// remove inspects ownership even for names generated by this process. Missing is
// established by a successful filtered list, never inferred from a transport error.
func (r *DockerRunner) remove(ctx context.Context, name string) error {
	raw, err := r.output(ctx, "ps", "-aq", "--filter", "name=^/"+name+"$")
	if err != nil {
		return err
	}
	if raw == "" {
		return nil
	}
	var objects []struct {
		ID     string
		Config struct{ Labels map[string]string }
	}
	raw, err = r.output(ctx, "inspect", name)
	if err != nil {
		return err
	}
	if json.Unmarshal([]byte(raw), &objects) != nil || len(objects) != 1 {
		return errors.New("invalid Docker inspect")
	}
	for k, v := range r.labels() {
		if objects[0].Config.Labels[k] != v {
			return errors.New("refusing to remove unrelated container")
		}
	}
	_, err = r.output(ctx, "rm", "--force", objects[0].ID)
	return err
}

func (r *DockerRunner) cleanup(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := r.remove(ctx, name)
	if err != nil {
		r.mu.Lock()
		r.cleanupFailure = err
		r.mu.Unlock()
	}
	return err
}

// Initialize must run under the Service's exclusive database lock. A second
// controller with the same root cannot reap a live first controller's tasks.
func (r *DockerRunner) Initialize(ctx context.Context) (err error) {
	r.mu.Lock()
	r.initialized = false
	r.mu.Unlock()
	filters := []string{"ps", "-aq"}
	for _, k := range []string{managedLabel, ownerLabel, rootLabel} {
		filters = append(filters, "--filter", "label="+k+"="+r.labels()[k])
	}
	ids, err := r.output(ctx, filters...)
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(ids) {
		// IDs need no name filter; inspect exact ID, compare labels, then remove.
		raw, e := r.output(ctx, "inspect", id)
		if e != nil {
			return e
		}
		var objects []struct {
			Name   string
			Config struct{ Labels map[string]string }
		}
		if json.Unmarshal([]byte(raw), &objects) != nil || len(objects) != 1 {
			return errors.New("invalid orphan inspection")
		}
		for k, v := range r.labels() {
			if objects[0].Config.Labels[k] != v {
				return errors.New("orphan ownership mismatch")
			}
		}
		if e = r.remove(ctx, strings.TrimPrefix(objects[0].Name, "/")); e != nil {
			return e
		}
	}
	// Resolve mutable operator image tag once: every task uses this immutable ID.
	image, err := r.output(ctx, "image", "inspect", "--format", "{{.Id}}", r.cfg.Image)
	if err != nil {
		return err
	}
	if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(image) {
		return errors.New("invalid runtime image ID")
	}
	r.cfg.Image = image
	probe, err := os.MkdirTemp(r.root, ".mapping-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(probe)
	marker := uuid.NewString()
	if err = os.WriteFile(filepath.Join(probe, "marker"), []byte(marker), 0600); err != nil {
		return err
	}
	host, err := r.hostPath(probe)
	if err != nil {
		return err
	}
	name := "easygo-" + uuid.NewString()
	defer func() {
		err = errors.Join(err, r.cleanup(name))
		if err == nil {
			r.mu.Lock()
			r.initialized = true
			r.mu.Unlock()
		}
	}()
	args := r.containerOptions(name, host, "")
	args = append(args, r.cfg.Image, "--verify-root", marker)
	if _, err = r.output(ctx, args...); err != nil {
		return err
	}
	output, err := r.output(ctx, "start", "--attach", name)
	if err != nil {
		return err
	}
	if output != marker {
		return errors.New("host_root does not map controller root or runtime UID cannot access workspace")
	}
	return nil
}

func (r *DockerRunner) Run(ctx context.Context, in Invocation, emit func(Event) error) (result Result, err error) {
	r.mu.Lock()
	broken := r.cleanupFailure
	ready := r.initialized
	r.mu.Unlock()
	if !ready {
		return result, errors.New("Docker runner requires successful initialization")
	}
	if broken != nil {
		return result, errors.New("Docker cleanup previously failed; operator recovery required")
	}
	p := in.Workflow.RuntimeSpec
	if p == nil || p.GatewayModel == "" || p.validate() != nil || p.Engine != in.Workflow.Engine || p.model() != in.Workflow.Model {
		return result, errors.New("Docker requires a matching gateway runtime profile")
	}
	args, err := engineArgs(in)
	if err != nil {
		return result, err
	}
	args, err = dockerRuntimeArgs(in.Workflow.Engine, args)
	if err != nil {
		return result, err
	}
	// The task cannot choose a mount path: only service-created UUID workspaces.
	rel, e := filepath.Rel(filepath.Join(r.root, "workspaces"), in.Workspace)
	if e != nil || strings.Contains(rel, string(filepath.Separator)) {
		return result, errors.New("invalid task workspace")
	}
	if _, e = uuid.Parse(rel); e != nil {
		return result, errors.New("invalid task workspace ID")
	}
	host, err := r.hostPath(in.Workspace)
	if err != nil {
		return result, err
	}
	if err = r.CheckDiskQuota(ctx, in.Workspace); err != nil {
		return result, err
	}
	home := filepath.Join(in.Workspace, ".workshop-home")
	for _, dir := range []string{home, filepath.Join(home, "codex"), filepath.Join(home, "claude")} {
		if err = mkdirNoSymlinks(dir, 0700); err != nil {
			return result, err
		}
		if err = noSymlinks(dir); err != nil {
			return result, err
		}
	}
	relayParent := filepath.Join(r.root, relayDirectory)
	if err = mkdirNoSymlinks(relayParent, 0700); err != nil {
		return result, err
	}
	if err = noSymlinks(relayParent); err != nil {
		return result, err
	}
	relay, err := os.MkdirTemp(relayParent, relayRunPrefix)
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(relay)
	relayHost, err := r.hostPath(relay)
	if err != nil {
		return result, err
	}
	socket := filepath.Join(relay, relaySocketName)
	_, key, stop, err := startModelRelayOn(ctx, r.gateway, in.Namespace, *p, "unix", socket, in.Crew)
	if err != nil {
		return result, err
	}
	defer stop()
	if err = os.Chmod(socket, 0600); err != nil {
		return result, err
	}
	env := map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": "/workspace/.workshop-home", "CODEX_HOME": "/workspace/.workshop-home/codex", "CLAUDE_CONFIG_DIR": "/workspace/.workshop-home/claude", "TMPDIR": "/tmp", "EASYGO_RELAY_SOCKET": runtimeRelay + "/" + relaySocketName}
	env["EASYGO_CREW_URL"] = "http://127.0.0.1:18080/crew"
	env["EASYGO_CREW_TOKEN"] = key
	childIn := in
	childIn.Workspace = runtimeWorkspace
	args, err = configureRuntimeEndpoint(childIn, args, env, "http://127.0.0.1:18080/v1", key, func(path string, v any) error {
		rel, e := filepath.Rel(runtimeWorkspace, path)
		if e != nil || !filepath.IsLocal(rel) {
			return errors.New("runtime config escaped workspace")
		}
		configured, configErr := dockerRuntimeConfig(in.Workflow.Engine, v)
		if configErr != nil {
			return configErr
		}
		return writeRuntimeJSON(filepath.Join(in.Workspace, rel), configured)
	})
	if err != nil {
		return result, err
	}
	if err = r.CheckDiskQuota(ctx, in.Workspace); err != nil {
		return result, err
	}
	name := "easygo-" + uuid.NewString()
	// Register cleanup before create: even ambiguous daemon create responses are reaped.
	defer func() {
		if e := r.cleanup(name); e != nil {
			err = errors.Join(err, errors.New("task container cleanup failed; admission disabled"))
		}
	}()
	options := r.taskContainerOptions(name, host, relayHost, filepath.Join(host, ".workshop-home"), in.Workflow.Policy)
	// Codex's nested bwrap cannot create user namespaces under this container's
	// security policy. Only this initialized Docker path delegates isolation to
	// the mandatory outer container and its workflow-specific workspace mounts.
	// Host CommandRunner and approval_policy=never remain unchanged.
	if in.Workflow.Engine == "codex" {
		replaced := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-c" && args[i+1] == `sandbox_mode="`+in.Workflow.Policy+`"` {
				args[i+1] = `sandbox_mode="danger-full-access"`
				replaced = true
			}
		}
		if !replaced {
			return result, errors.New("missing native Codex sandbox override")
		}
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		options = append(options, "--env", k+"="+env[k])
	}
	options = append(options, r.cfg.Image, in.Workflow.Engine)
	options = append(options, args...)
	if _, err = r.output(ctx, options...); err != nil {
		return result, err
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	defer stopMonitor()
	quotaDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(time.Duration(r.cfg.DiskPollMS) * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-monitorCtx.Done():
				quotaDone <- nil
				return
			case <-ticker.C:
				if e := r.CheckDiskQuota(monitorCtx, in.Workspace); e != nil {
					if monitorCtx.Err() != nil {
						quotaDone <- nil
						return
					}
					cancelRun()
					quotaDone <- e
					return
				}
			}
		}
	}()
	result, err = runNative(runCtx, in.Workflow.Engine, r.maxOutput, []string{key}, emit, func(childCtx context.Context, stdout, stderr io.Writer) error {
		return r.command(childCtx, strings.NewReader(invocationPrompt(in)), stdout, stderr, "start", "--attach", "--interactive", name)
	})
	stopMonitor()
	if quotaErr := <-quotaDone; quotaErr != nil {
		return result, quotaErr
	}
	// A short-lived process can finish between polls. Cancellation must not
	// bypass the final quota check, but the scan still has a bounded lifetime.
	budget := 30 * time.Second
	if r.finalQuotaTimeout != 0 {
		budget = r.finalQuotaTimeout
	}
	quotaCtx, cancelQuota := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancelQuota()
	if quotaErr := r.CheckDiskQuota(quotaCtx, in.Workspace); quotaErr != nil {
		if errors.Is(quotaErr, context.DeadlineExceeded) && errors.Is(quotaCtx.Err(), context.DeadlineExceeded) {
			return result, fmt.Errorf("%w: final scan: %w", ErrDiskQuotaScanFailed, quotaErr)
		}
		return result, quotaErr
	}
	return result, err
}
