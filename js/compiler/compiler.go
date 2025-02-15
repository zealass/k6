/*
 *
 * k6 - a next-generation load testing tool
 * Copyright (C) 2017 Load Impact
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, either version 3 of the
 * License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package compiler

import (
	_ "embed" // we need this for embedding Babel
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja/parser"
	"github.com/sirupsen/logrus"

	"go.k6.io/k6/lib"
)

//go:embed lib/babel.min.js
var babelSrc string //nolint:gochecknoglobals

var (
	DefaultOpts = map[string]interface{}{
		// "presets": []string{"latest"},
		"plugins": []interface{}{
			// es2015 https://github.com/babel/babel/blob/v6.26.0/packages/babel-preset-es2015/src/index.js
			// in goja
			// []interface{}{"transform-es2015-template-literals", map[string]interface{}{"loose": false, "spec": false}},
			// "transform-es2015-literals", // in goja
			// "transform-es2015-function-name", // in goja
			// []interface{}{"transform-es2015-arrow-functions", map[string]interface{}{"spec": false}}, // in goja
			// "transform-es2015-block-scoped-functions", // in goja
			[]interface{}{"transform-es2015-classes", map[string]interface{}{"loose": false}},
			"transform-es2015-object-super",
			// "transform-es2015-shorthand-properties", // in goja
			// "transform-es2015-duplicate-keys", // in goja
			// []interface{}{"transform-es2015-computed-properties", map[string]interface{}{"loose": false}}, // in goja
			// "transform-es2015-for-of", // in goja
			// "transform-es2015-sticky-regex", // in goja
			// "transform-es2015-unicode-regex", // in goja
			// "check-es2015-constants", // in goja
			// []interface{}{"transform-es2015-spread", map[string]interface{}{"loose": false}}, // in goja
			// "transform-es2015-parameters", // in goja
			// []interface{}{"transform-es2015-destructuring", map[string]interface{}{"loose": false}}, // in goja
			// "transform-es2015-block-scoping", // in goja
			// "transform-es2015-typeof-symbol", // in goja
			// all the other module plugins are just dropped
			[]interface{}{"transform-es2015-modules-commonjs", map[string]interface{}{"loose": false}},
			// "transform-regenerator", // Doesn't really work unless regeneratorRuntime is also added

			// es2016 https://github.com/babel/babel/blob/v6.26.0/packages/babel-preset-es2016/src/index.js
			"transform-exponentiation-operator",

			// es2017 https://github.com/babel/babel/blob/v6.26.0/packages/babel-preset-es2017/src/index.js
			// "syntax-trailing-function-commas", // in goja
			// "transform-async-to-generator", // Doesn't really work unless regeneratorRuntime is also added
		},
		"ast":           false,
		"sourceMaps":    false,
		"babelrc":       false,
		"compact":       false,
		"retainLines":   true,
		"highlightCode": false,
	}

	onceBabelCode      sync.Once     // nolint:gochecknoglobals
	globalBabelCode    *goja.Program // nolint:gochecknoglobals
	globalBabelCodeErr error         // nolint:gochecknoglobals
	onceBabel          sync.Once     // nolint:gochecknoglobals
	globalBabel        *babel        // nolint:gochecknoglobals
)

const sourceMapURLFromBabel = "k6://internal-should-not-leak/file.map"

// A Compiler compiles JavaScript source code (ES5.1 or ES6) into a goja.Program
type Compiler struct {
	logger  logrus.FieldLogger
	babel   *babel
	Options Options
}

// New returns a new Compiler
func New(logger logrus.FieldLogger) *Compiler {
	return &Compiler{logger: logger}
}

// initializeBabel initializes a separate (non-global) instance of babel specifically for this Compiler.
// An error is returned only if babel itself couldn't be parsed/run which should never be possible.
func (c *Compiler) initializeBabel() error {
	var err error
	if c.babel == nil {
		c.babel, err = newBabel()
	}
	return err
}

// Transform the given code into ES5
func (c *Compiler) Transform(src, filename string, inputSrcMap []byte) (code string, srcMap []byte, err error) {
	if c.babel == nil {
		onceBabel.Do(func() {
			globalBabel, err = newBabel()
		})
		c.babel = globalBabel
	}
	if err != nil {
		return
	}

	code, srcMap, err = c.babel.transformImpl(c.logger, src, filename, c.Options.SourceMapLoader != nil, inputSrcMap)
	return
}

// Options are options to the compiler
type Options struct {
	CompatibilityMode lib.CompatibilityMode
	SourceMapLoader   func(string) ([]byte, error)
	Strict            bool
}

// compilationState is helper struct to keep the state of a compilation
type compilationState struct {
	// set when we couldn't load external source map so we can try parsing without loading it
	couldntLoadSourceMap bool
	// srcMap is the current full sourceMap that has been generated read so far
	srcMap []byte
	main   bool

	compiler *Compiler
}

// Compile the program in the given CompatibilityMode, wrapping it between pre and post code
func (c *Compiler) Compile(src, filename string, main bool) (*goja.Program, string, error) {
	return c.compileImpl(src, filename, main, c.Options.CompatibilityMode, nil)
}

// sourceMapLoader is to be used with goja's WithSourceMapLoader
// it not only gets the file from disk in the simple case, but also returns it if the map was generated from babel
// additioanlly it fixes off by one error in commonjs dependencies due to having to wrap them in a function.
func (c *compilationState) sourceMapLoader(path string) ([]byte, error) {
	if path == sourceMapURLFromBabel {
		if !c.main {
			return c.increaseMappingsByOne(c.srcMap)
		}
		return c.srcMap, nil
	}
	var err error
	c.srcMap, err = c.compiler.Options.SourceMapLoader(path)
	if err != nil {
		c.couldntLoadSourceMap = true
		return nil, err
	}
	if !c.main {
		return c.increaseMappingsByOne(c.srcMap)
	}
	return c.srcMap, err
}

func (c *Compiler) compileImpl(
	src, filename string, main bool, compatibilityMode lib.CompatibilityMode, srcMap []byte,
) (*goja.Program, string, error) {
	code := src
	state := compilationState{srcMap: srcMap, compiler: c, main: main}
	if !main { // the lines in the sourcemap (if available) will be fixed by increaseMappingsByOne
		code = "(function(module, exports){\n" + code + "\n})\n"
	}
	opts := parser.WithDisableSourceMaps
	if c.Options.SourceMapLoader != nil {
		opts = parser.WithSourceMapLoader(state.sourceMapLoader)
	}
	ast, err := parser.ParseFile(nil, filename, code, 0, opts)

	if state.couldntLoadSourceMap {
		state.couldntLoadSourceMap = false // reset
		// we probably don't want to abort scripts which have source maps but they can't be found,
		// this also will be a breaking change, so if we couldn't we retry with it disabled
		c.logger.WithError(err).Warnf("Couldn't load source map for %s", filename)
		ast, err = parser.ParseFile(nil, filename, code, 0, parser.WithDisableSourceMaps)
	}
	if err != nil {
		if compatibilityMode == lib.CompatibilityModeExtended {
			code, state.srcMap, err = c.Transform(src, filename, state.srcMap)
			if err != nil {
				return nil, code, err
			}
			// the compatibility mode "decreases" here as we shouldn't transform twice
			return c.compileImpl(code, filename, main, lib.CompatibilityModeBase, state.srcMap)
		}
		return nil, code, err
	}
	pgm, err := goja.CompileAST(ast, c.Options.Strict)
	return pgm, code, err
}

type babel struct {
	vm        *goja.Runtime
	this      goja.Value
	transform goja.Callable
	m         sync.Mutex
}

func newBabel() (*babel, error) {
	onceBabelCode.Do(func() {
		globalBabelCode, globalBabelCodeErr = goja.Compile("<internal/k6/compiler/lib/babel.min.js>", babelSrc, false)
	})
	if globalBabelCodeErr != nil {
		return nil, globalBabelCodeErr
	}
	vm := goja.New()
	_, err := vm.RunProgram(globalBabelCode)
	if err != nil {
		return nil, err
	}

	this := vm.Get("Babel")
	bObj := this.ToObject(vm)
	result := &babel{vm: vm, this: this}
	if err = vm.ExportTo(bObj.Get("transform"), &result.transform); err != nil {
		return nil, err
	}

	return result, err
}

// increaseMappingsByOne increases the lines in the sourcemap by line so that it fixes the case where we need to wrap a
// required file in a function to support/emulate commonjs
func (c *compilationState) increaseMappingsByOne(sourceMap []byte) ([]byte, error) {
	var err error
	m := make(map[string]interface{})
	if err = json.Unmarshal(sourceMap, &m); err != nil {
		return nil, err
	}
	mappings, ok := m["mappings"]
	if !ok {
		// no mappings, no idea what this will do, but just return it as technically we can have sourcemap with sections
		// TODO implement incrementing of `offset` in the sections? to support that case as well
		// see https://sourcemaps.info/spec.html#h.n05z8dfyl3yh
		//
		// TODO (kind of alternatively) drop the newline in the "commonjs" wrapping and have only the first line wrong
		// and drop this whole function
		return sourceMap, nil
	}
	if str, ok := mappings.(string); ok {
		// ';' is the separator between lines so just adding 1 will make all mappings be for the line after which they were
		// originally
		m["mappings"] = ";" + str
	} else {
		// we have mappings but it's not a string - this is some kind of error
		// we still won't abort the test but just not load the sourcemap
		c.couldntLoadSourceMap = true
		return nil, errors.New(`missing "mappings" in sourcemap`)
	}

	return json.Marshal(m)
}

// transformImpl the given code into ES5, while synchronizing to ensure only a single
// bundle instance / Goja VM is in use at a time.
func (b *babel) transformImpl(
	logger logrus.FieldLogger, src, filename string, sourceMapsEnabled bool, inputSrcMap []byte,
) (string, []byte, error) {
	b.m.Lock()
	defer b.m.Unlock()
	opts := make(map[string]interface{})
	for k, v := range DefaultOpts {
		opts[k] = v
	}
	if sourceMapsEnabled {
		// given that the source map should provide accurate lines(and columns), this option isn't needed
		// it also happens to make very long and awkward lines, especially around import/exports and definitely a lot
		// less readable overall. Hopefully it also has some performance improvement not trying to keep the same lines
		opts["retainLines"] = false
		opts["sourceMaps"] = true
		if inputSrcMap != nil {
			srcMap := new(map[string]interface{})
			if err := json.Unmarshal(inputSrcMap, &srcMap); err != nil {
				return "", nil, err
			}
			opts["inputSourceMap"] = srcMap
		}
	}
	opts["filename"] = filename

	startTime := time.Now()
	v, err := b.transform(b.this, b.vm.ToValue(src), b.vm.ToValue(opts))
	if err != nil {
		return "", nil, err
	}
	logger.WithField("t", time.Since(startTime)).Debug("Babel: Transformed")

	vO := v.ToObject(b.vm)
	var code string
	if err = b.vm.ExportTo(vO.Get("code"), &code); err != nil {
		return code, nil, err
	}
	if !sourceMapsEnabled {
		return code, nil, nil
	}

	// this is to make goja try to load a sourcemap.
	// it is a special url as it should never leak outside of this code
	// additionally the alternative support from babel is to embed *the whole* sourcemap at the end
	code += "\n//# sourceMappingURL=" + sourceMapURLFromBabel
	stringify, err := b.vm.RunString("(function(m) { return JSON.stringify(m)})")
	if err != nil {
		return code, nil, err
	}
	c, _ := goja.AssertFunction(stringify)
	mapAsJSON, err := c(goja.Undefined(), vO.Get("map"))
	if err != nil {
		return code, nil, err
	}
	return code, []byte(mapAsJSON.String()), nil
}

// Pool is a pool of compilers so it can be used easier in parallel tests as they have their own babel.
type Pool struct {
	c chan *Compiler
}

// NewPool creates a Pool that will be using the provided logger and will preallocate (in parallel)
// the count of compilers each with their own babel.
func NewPool(logger logrus.FieldLogger, count int) *Pool {
	c := &Pool{
		c: make(chan *Compiler, count),
	}
	go func() {
		for i := 0; i < count; i++ {
			go func() {
				co := New(logger)
				err := co.initializeBabel()
				if err != nil {
					panic(err)
				}
				c.Put(co)
			}()
		}
	}()

	return c
}

// Get a compiler from the pool.
func (c *Pool) Get() *Compiler {
	return <-c.c
}

// Put a compiler back in the pool.
func (c *Pool) Put(co *Compiler) {
	c.c <- co
}
