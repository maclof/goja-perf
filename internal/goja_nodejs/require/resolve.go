package require

import (
	"encoding/json"
	"errors"
	"path"
	"strings"

	js "github.com/maclof/goja-perf"
)

// Node.js module search algorithm:
// https://nodejs.org/api/modules.html#modules_all_together
func (r *RequireModule) resolve(modpath string) (module *js.Object, err error) {
	origPath, modpath := modpath, filepathClean(modpath)
	if modpath == "" {
		return nil, IllegalModuleNameError
	}

	module, err = r.loadNative(modpath)
	if err == nil {
		return module, nil
	}

	var start string
	err = nil
	if path.IsAbs(origPath) {
		start = "/"
	} else {
		start = r.getCurrentModulePath()
	}

	p := path.Join(start, modpath)
	if strings.HasPrefix(origPath, "./") || strings.HasPrefix(origPath, "/") ||
		strings.HasPrefix(origPath, "../") || origPath == "." || origPath == ".." {
		if module = r.modules[p]; module != nil {
			return module, nil
		}
		module, err = r.loadAsFileOrDirectory(p)
		if err == nil && module != nil {
			r.modules[p] = module
		}
	} else {
		if module = r.nodeModules[p]; module != nil {
			return module, nil
		}
		module, err = r.loadNodeModules(modpath, start)
		if err == nil && module != nil {
			r.nodeModules[p] = module
		}
	}

	if module == nil && err == nil {
		err = InvalidModuleError
	}
	return module, err
}

func (r *RequireModule) loadNative(modulePath string) (*js.Object, error) {
	if module := r.modules[modulePath]; module != nil {
		return module, nil
	}

	loader := r.r.native[modulePath]
	if loader == nil {
		loader = native[modulePath]
	}
	if loader == nil {
		return nil, InvalidModuleError
	}

	module := r.createModuleObject()
	r.modules[modulePath] = module
	loader(r.runtime, module)
	return module, nil
}

func (r *RequireModule) loadAsFileOrDirectory(modulePath string) (*js.Object, error) {
	if module, err := r.loadAsFile(modulePath); module != nil || err != nil {
		return module, err
	}
	return r.loadAsDirectory(modulePath)
}

func (r *RequireModule) loadAsFile(modulePath string) (*js.Object, error) {
	if module, err := r.loadModule(modulePath); module != nil || err != nil {
		return module, err
	}
	if module, err := r.loadModule(modulePath + ".js"); module != nil || err != nil {
		return module, err
	}
	return r.loadModule(modulePath + ".json")
}

func (r *RequireModule) loadIndex(modulePath string) (*js.Object, error) {
	if module, err := r.loadModule(path.Join(modulePath, "index.js")); module != nil || err != nil {
		return module, err
	}
	return r.loadModule(path.Join(modulePath, "index.json"))
}

func (r *RequireModule) loadAsDirectory(modulePath string) (*js.Object, error) {
	buf, err := r.r.getSource(path.Join(modulePath, "package.json"))
	if err != nil {
		return r.loadIndex(modulePath)
	}
	var pkg struct{ Main string }
	if err = json.Unmarshal(buf, &pkg); err != nil || pkg.Main == "" {
		return r.loadIndex(modulePath)
	}
	mainPath := path.Join(modulePath, pkg.Main)
	if module, err := r.loadAsFile(mainPath); module != nil || err != nil {
		return module, err
	}
	return r.loadIndex(mainPath)
}

func (r *RequireModule) loadNodeModules(modpath, start string) (*js.Object, error) {
	for _, dir := range r.r.globalFolders {
		if module, err := r.loadAsFileOrDirectory(path.Join(dir, modpath)); module != nil || err != nil {
			return module, err
		}
	}
	for {
		moduleDir := start
		if path.Base(start) != "node_modules" {
			moduleDir = path.Join(start, "node_modules")
		}
		if module, err := r.loadAsFileOrDirectory(path.Join(moduleDir, modpath)); module != nil || err != nil {
			return module, err
		}
		if start == ".." {
			break
		}
		parent := path.Dir(start)
		if parent == start {
			break
		}
		start = parent
	}
	return nil, InvalidModuleError
}

func (r *RequireModule) getCurrentModulePath() string {
	var buf [2]js.StackFrame
	frames := r.runtime.CaptureCallStack(2, buf[:0])
	if len(frames) < 2 {
		return "."
	}
	return path.Dir(frames[1].SrcName())
}

func (r *RequireModule) createModuleObject() *js.Object {
	module := r.runtime.NewObject()
	module.Set("exports", r.runtime.NewObject())
	return module
}

func (r *RequireModule) loadModule(modulePath string) (*js.Object, error) {
	if module := r.modules[modulePath]; module != nil {
		return module, nil
	}
	module := r.createModuleObject()
	r.modules[modulePath] = module
	err := r.loadModuleFile(modulePath, module)
	if err != nil {
		delete(r.modules, modulePath)
		if errors.Is(err, ModuleFileDoesNotExistError) {
			err = nil
		}
		return nil, err
	}
	return module, nil
}

func (r *RequireModule) loadModuleFile(modulePath string, module *js.Object) error {
	prg, err := r.r.getCompiledSource(modulePath)
	if err != nil {
		return err
	}
	value, err := r.runtime.RunProgram(prg)
	if err != nil {
		return err
	}
	call, ok := js.AssertFunction(value)
	if !ok {
		return InvalidModuleError
	}
	exports := module.Get("exports")
	_, err = call(exports, exports, r.runtime.Get("require"), module)
	return err
}
