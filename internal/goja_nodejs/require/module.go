package require

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"sync"
	"syscall"
	"text/template"

	js "github.com/maclof/goja-perf"
	"github.com/maclof/goja-perf/parser"
)

type ModuleLoader func(*js.Runtime, *js.Object)

// SourceLoader returns file data for a path. It must return
// ModuleFileDoesNotExistError when the path does not exist or is a directory.
type SourceLoader func(path string) ([]byte, error)

var (
	InvalidModuleError     = errors.New("Invalid module")
	IllegalModuleNameError = errors.New("Illegal module name")

	ModuleFileDoesNotExistError = errors.New("module file does not exist")
)

var native map[string]ModuleLoader

// Registry contains a cache of compiled modules which can be shared by runtimes.
type Registry struct {
	sync.Mutex
	native   map[string]ModuleLoader
	compiled map[string]*js.Program

	srcLoader     SourceLoader
	globalFolders []string
}

type RequireModule struct {
	r           *Registry
	runtime     *js.Runtime
	modules     map[string]*js.Object
	nodeModules map[string]*js.Object
}

func NewRegistry(opts ...Option) *Registry {
	r := &Registry{}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func NewRegistryWithLoader(srcLoader SourceLoader) *Registry {
	return NewRegistry(WithLoader(srcLoader))
}

type Option func(*Registry)

// WithLoader configures the source and external source-map loader used by require.
func WithLoader(srcLoader SourceLoader) Option {
	return func(r *Registry) {
		r.srcLoader = srcLoader
	}
}

// WithGlobalFolders appends paths searched for modules not found elsewhere.
func WithGlobalFolders(globalFolders ...string) Option {
	return func(r *Registry) {
		r.globalFolders = globalFolders
	}
}

// Enable adds require() to runtime.
func (r *Registry) Enable(runtime *js.Runtime) *RequireModule {
	rrt := &RequireModule{
		r:           r,
		runtime:     runtime,
		modules:     make(map[string]*js.Object),
		nodeModules: make(map[string]*js.Object),
	}
	runtime.Set("require", rrt.require)
	return rrt
}

func (r *Registry) RegisterNativeModule(name string, loader ModuleLoader) {
	r.Lock()
	defer r.Unlock()
	if r.native == nil {
		r.native = make(map[string]ModuleLoader)
	}
	r.native[filepathClean(name)] = loader
}

// DefaultSourceLoader reads module files from the host filesystem.
func DefaultSourceLoader(filename string) ([]byte, error) {
	data, err := os.ReadFile(filepath.FromSlash(filename))
	if err != nil && (os.IsNotExist(err) || errors.Is(err, syscall.EISDIR)) {
		err = ModuleFileDoesNotExistError
	}
	return data, err
}

func (r *Registry) getSource(p string) ([]byte, error) {
	srcLoader := r.srcLoader
	if srcLoader == nil {
		srcLoader = DefaultSourceLoader
	}
	return srcLoader(p)
}

func (r *Registry) getCompiledSource(p string) (*js.Program, error) {
	r.Lock()
	defer r.Unlock()

	prg := r.compiled[p]
	if prg != nil {
		return prg, nil
	}
	buf, err := r.getSource(p)
	if err != nil {
		return nil, err
	}
	source := string(buf)
	if path.Ext(p) == ".json" {
		source = "module.exports = JSON.parse('" + template.JSEscapeString(source) + "')"
	}
	source = "(function(exports, require, module) {" + source + "\n})"
	parsed, err := js.Parse(p, source, parser.WithSourceMapLoader(r.srcLoader))
	if err != nil {
		return nil, err
	}
	prg, err = js.CompileAST(parsed, false)
	if err == nil {
		if r.compiled == nil {
			r.compiled = make(map[string]*js.Program)
		}
		r.compiled[p] = prg
	}
	return prg, err
}

func (r *RequireModule) require(call js.FunctionCall) js.Value {
	ret, err := r.Require(call.Argument(0).String())
	if err != nil {
		if _, ok := err.(*js.Exception); !ok {
			panic(r.runtime.NewGoError(err))
		}
		panic(err)
	}
	return ret
}

func filepathClean(p string) string {
	return path.Clean(p)
}

// Require imports a module from Go code.
func (r *RequireModule) Require(p string) (js.Value, error) {
	module, err := r.resolve(p)
	if err != nil {
		return nil, err
	}
	return module.Get("exports"), nil
}

func Require(runtime *js.Runtime, name string) js.Value {
	if require, ok := js.AssertFunction(runtime.Get("require")); ok {
		module, err := require(js.Undefined(), runtime.ToValue(name))
		if err != nil {
			panic(err)
		}
		return module
	}
	panic(runtime.NewTypeError("Please enable require for this runtime using new(require.Registry).Enable(runtime)"))
}

func RegisterNativeModule(name string, loader ModuleLoader) {
	if native == nil {
		native = make(map[string]ModuleLoader)
	}
	native[filepathClean(name)] = loader
}
