package gpu

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"

	"github.com/containerd/nri/pkg/api"
	"github.com/kcrow-io/kcrow/pkg/k8s"
	"github.com/kcrow-io/kcrow/pkg/oci"
	"github.com/kcrow-io/kcrow/pkg/util"
	"github.com/kcrow-io/kcrow/pkg/xpu/gpu/image"

	// nvidiaconfig "github.com/NVIDIA/nvidia-container-toolkit/internal/config"

	rspec "github.com/opencontainers/runtime-spec/specs-go"
	"k8s.io/klog/v2"
)

const (
	// only legacy mode will process
	legacyMode = "legacy"
	cdiMode    = "cdi"
	// NVIDIAContainerRuntimeHookExecutable is the executable name for the NVIDIA Container Runtime Hook
	NVIDIAContainerRuntimeHookExecutable = "nvidia-container-runtime-hook"
	// NVIDIAContainerToolkitExecutable is the executable name for the NVIDIA Container Toolkit (an alias for the NVIDIA Container Runtime Hook)
	NVIDIAContainerToolkitExecutable = "nvidia-container-toolkit"
	NvidiaContainerRuntimeHookPath   = "/usr/bin/nvidia-container-runtime-hook"
)

var (
	name          = "nvidiagpu"
	annotationKey = "nvidia.gpu.kcrow.io"
)

type gpuPath struct {
	HookPath string `json:"hookpath" yaml:"hookpath"`
	LibPath  string `json:"libpath" yaml:"libpath"`
}

type Gpu struct {
	rmctl *k8s.RuntimeManage

	// runtime - value
	runtime map[string]*gpuPath

	mu sync.RWMutex
}

func New(rm *k8s.RuntimeManage) *Gpu {
	gpu := &Gpu{
		runtime: map[string]*gpuPath{},
		rmctl:   rm,
	}
	rm.Registe(gpu)
	return gpu
}

func (g *Gpu) Name() string {
	return name
}

func (g *Gpu) RuntimeUpdate(ri *k8s.RuntimeItem) {
	klog.Infof("process RuntimeUpdate")
	if ri == nil {
		return
	}
	var p = &gpuPath{}

	for k, v := range ri.No.Annotations {
		if strings.ToLower(k) == annotationKey {
			err := json.Unmarshal([]byte(v), p)
			if err != nil {
				klog.Warningf("unmarshal runtime %s annotation %s failed: %v", ri.No.Name, annotationKey, err)
			} else {
				g.mu.Lock()
				g.runtime[ri.No.Name] = p
				g.mu.Unlock()
				return
			}
		}
	}
}

// TODO
func (g *Gpu) Process(ctx context.Context, im *oci.Item) error {
	klog.Infof("start process")
	if im == nil || im.Ct == nil {
		klog.Warningf("not found container info")
		return nil
	}
	var (
		ct = im.Ct
		// csv 暂不支持
		mode = legacyMode
	)
	visibleDevices := util.GetValueFromEnvByKey(ct.Env, visibleDevicesEnvvar)
	if visibleDevices == "" {
		klog.V(2).Infof("gpu process, no env %s found, skip", visibleDevicesEnvvar)
		return nil
	}

	cuda, err := image.New(image.WithEnv(ct.Env), image.WithMounts(toMount(ct.Mounts)))
	if err != nil {
		return err
	}
	if cuda.OnlyFullyQualifiedCDIDevices() {
		mode = cdiMode
	}
	if mode != legacyMode {
		klog.Infof("container '%s' is not legacy mode, skip", name)
		return nil
	}

	// mode modify
	if ct.Hooks != nil && ct.Hooks.Prestart != nil {
		for _, hook := range ct.Hooks.Prestart {
			hook := hook
			if isNVIDIAContainerRuntimeHook(hook) {
				klog.Infof("Existing nvidia prestart hook (%v) found in OCI spec", hook.Path)
				return nil
			}
		}
	}
	// TODO 如何自定义路径
	path := NvidiaContainerRuntimeHookPath
	klog.Infof("Using default prestart hook path: %v", path)
	args := []string{filepath.Base(path)}
	if im.Adjust.Hooks == nil {
		im.Adjust.Hooks = &api.Hooks{}
	}
	if im.Adjust.Hooks.Prestart == nil {
		im.Adjust.Hooks.Prestart = []*api.Hook{}
	}
	im.Adjust.Hooks.Prestart = append(im.Adjust.Hooks.Prestart, &api.Hook{
		Path: path,
		Args: append(args, "prestart"),
	})
	// TODO: graphics modify
	// TODO: featuregate modify

	// TODO support more runtime
	klog.Infof("process nvidiagpu device, in vm runtime: %v", g.rmctl.Isvm(im.Sb.RuntimeHandler))

	return nil

}

func toMount(m []*api.Mount) []rspec.Mount {
	var dst = make([]rspec.Mount, len(m))
	for i, v := range m {
		dst[i] = v.ToOCI(nil)
	}
	return dst
}

func isNVIDIAContainerRuntimeHook(hook *api.Hook) bool {
	if hook == nil {
		return false
	}
	bins := map[string]struct{}{
		NVIDIAContainerRuntimeHookExecutable: {},
		NVIDIAContainerToolkitExecutable:     {},
	}

	_, exists := bins[filepath.Base(hook.Path)]

	return exists
}
