package asp

import (
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"

	"core"
)

// A few sneaky globals for when we don't have a scope handy
var stringMethods, dictMethods, configMethods map[string]*pyFunc

const subincludePackageName = "_remote"

// A nativeFunc is a function that implements a builtin function natively.
type nativeFunc func(*scope, []pyObject) pyObject

// registerBuiltins sets up the "special" builtins that map to native code.
func registerBuiltins(s *scope) {
	// varargs is set here as an optimisation; we don't have to populate all 30-odd arguments
	// into the argument map. We do have to be careful about reading them though.
	setNativeCode(s, "build_rule", buildRule)
	setNativeCode(s, "subinclude", subinclude)
	setNativeCode(s, "package", pkg).kwargs = true
	setNativeCode(s, "sorted", sorted)
	setNativeCode(s, "isinstance", isinstance)
	setNativeCode(s, "callable", callable)
	setNativeCode(s, "range", pyRange)
	setNativeCode(s, "enumerate", enumerate)
	setNativeCode(s, "zip", zip).varargs = true
	setNativeCode(s, "len", lenFunc)
	setNativeCode(s, "glob", glob)
	setNativeCode(s, "bool", boolType)
	setNativeCode(s, "str", strType)
	setNativeCode(s, "join_path", joinPath).varargs = true
	setNativeCode(s, "get_base_path", packageName)
	setNativeCode(s, "package_name", packageName)
	setNativeCode(s, "get_labels", getLabels)
	setNativeCode(s, "add_dep", addDep)
	setNativeCode(s, "add_out", addOut)
	setNativeCode(s, "add_licence", addLicence)
	setNativeCode(s, "get_command", getCommand)
	setNativeCode(s, "set_command", setCommand)
	stringMethods = map[string]*pyFunc{
		"join":       setNativeCode(s, "join", strJoin),
		"split":      setNativeCode(s, "split", strSplit),
		"replace":    setNativeCode(s, "replace", strReplace),
		"partition":  setNativeCode(s, "partition", strPartition),
		"rpartition": setNativeCode(s, "rpartition", strRPartition),
		"startswith": setNativeCode(s, "startswith", strStartsWith),
		"endswith":   setNativeCode(s, "endswith", strEndsWith),
		"lstrip":     setNativeCode(s, "lstrip", strLStrip),
		"rstrip":     setNativeCode(s, "rstrip", strRStrip),
		"strip":      s.Lookup("strip").(*pyFunc),
		"find":       setNativeCode(s, "find", strFind),
		"rfind":      setNativeCode(s, "find", strRFind),
		"format":     setNativeCode(s, "format", strFormat),
		"count":      setNativeCode(s, "count", strCount),
	}
	stringMethods["format"].kwargs = true
	dictMethods = map[string]*pyFunc{
		"get":        setNativeCode(s, "get", dictGet),
		"setdefault": s.Lookup("setdefault").(*pyFunc),
		"keys":       setNativeCode(s, "keys", dictKeys),
		"items":      setNativeCode(s, "items", dictItems),
		"values":     setNativeCode(s, "values", dictValues),
		"copy":       setNativeCode(s, "copy", dictCopy),
	}
	configMethods = map[string]*pyFunc{
		"get":        setNativeCode(s, "config_get", configGet),
		"setdefault": s.Lookup("setdefault").(*pyFunc),
	}
	setLogCode(s, "debug", log.Debug)
	setLogCode(s, "info", log.Info)
	setLogCode(s, "notice", log.Notice)
	setLogCode(s, "warning", log.Warning)
	setLogCode(s, "error", log.Errorf)
	setLogCode(s, "fatal", log.Fatalf)
}

// registerSubincludePackage sets up the package for remote subincludes.
func registerSubincludePackage(s *scope) {
	var pkg *core.Package
	if pkg = s.state.Graph.Package(subincludePackageName); pkg == nil {
		pkg = core.NewPackage(subincludePackageName)
		s.state.Graph.AddPackage(pkg)
	}
	s.interpreter.subincludeScope = s.NewPackagedScope(pkg)
	// Always counts as being in callback mode (i.e. the package is already parsed and we are adding individual targets later).
	s.interpreter.subincludeScope.Callback = true
	// Another small hack - replace the code for these two with native code, must be done after the
	// declarations which are in misc_rules.
	buildRule := s.Lookup("build_rule").(*pyFunc)
	f := setNativeCode(s, "filegroup", filegroup)
	f.args = buildRule.args
	f.argIndices = buildRule.argIndices
	f.defaults = buildRule.defaults
	f.constants = buildRule.constants
	f.types = buildRule.types
	f = setNativeCode(s, "hash_filegroup", hashFilegroup)
	f.args = buildRule.args
	f.argIndices = buildRule.argIndices
	f.defaults = buildRule.defaults
	f.constants = buildRule.constants
	f.types = buildRule.types
}

func setNativeCode(s *scope, name string, code nativeFunc) *pyFunc {
	f := s.Lookup(name).(*pyFunc)
	f.nativeCode = code
	f.code = nil // Might as well save a little memory here
	return f
}

// setLogCode specialises setNativeCode for handling the log functions (of which there are a few)
func setLogCode(s *scope, name string, f func(format string, args ...interface{})) {
	setNativeCode(s, name, func(s *scope, args []pyObject) pyObject {
		if str, ok := args[0].(pyString); ok {
			l := make([]interface{}, len(args))
			for i, arg := range args {
				l[i] = arg
			}
			f("//%s: %s", s.pkg.Filename, fmt.Sprintf(string(str), l[1:]...))
			return None
		}
		f("//%s: %s", s.pkg.Filename, args)
		return None
	}).varargs = true
}

// buildRule implements the build_rule() builtin function.
// This is the main interface point; every build rule ultimately calls this to add
// new objects to the build graph.
func buildRule(s *scope, args []pyObject) pyObject {
	// We need to set various defaults from config here; it is useful to put it on the rule but not often so
	// because most rules pass them through anyway.
	// TODO(peterebden): when we get rid of the old parser, put these defaults on all the build rules and
	//                   get rid of this.
	config := s.Lookup("CONFIG").(*pyConfig)
	args[11] = defaultFromConfig(config, args[11], "DEFAULT_VISIBILITY")
	args[15] = defaultFromConfig(config, args[15], "DEFAULT_TESTONLY")
	args[30] = defaultFromConfig(config, args[30], "DEFAULT_LICENCES")
	args[20] = defaultFromConfig(config, args[20], "BUILD_SANDBOX")
	args[21] = defaultFromConfig(config, args[21], "TEST_SANDBOX")
	target := createTarget(s, args)
	s.Assert(s.pkg.Target(target.Label.Name) == nil, "Duplicate build target in %s: %s", s.pkg.Name, target.Label.Name)
	s.pkg.AddTarget(target)
	populateTarget(s, target, args)
	if s.Callback {
		// We are in a post-build function, so add the target directly to the graph now.
		log.Debug("Adding new target %s directly to graph", target.Label)
		target.AddedPostBuild = true
		s.state.Graph.AddTarget(target)
		s.pkg.MarkTargetModified(target)
	}
	return pyString(":" + target.Label.Name)
}

// filegroup implements the filegroup() builtin.
func filegroup(s *scope, args []pyObject) pyObject {
	args[1] = filegroupCommand
	return buildRule(s, args)
}

// hashFilegroup implements the hash_filegroup() builtin.
func hashFilegroup(s *scope, args []pyObject) pyObject {
	args[1] = hashFilegroupCommand
	return buildRule(s, args)
}

// defaultFromConfig sets a default value from the config if the property isn't set.
func defaultFromConfig(config *pyConfig, arg pyObject, name string) pyObject {
	if arg == nil || arg == None {
		return config.Get(name, arg)
	}
	return arg
}

// pkg implements the package() builtin function.
func pkg(s *scope, args []pyObject) pyObject {
	s.Assert(s.pkg.NumTargets() == 0, "package() must be called before any build targets are defined")
	c, ok := s.Lookup("CONFIG").(*pyConfig)
	s.Assert(ok, "CONFIG object has been altered")
	for k, v := range s.locals {
		k = strings.ToUpper(k)
		s.Assert(c.Get(k, nil) != nil, "error calling package(): %s is not a known config value", k)
		c.IndexAssign(pyString(k), v)
	}
	return None
}

// tagName applies the given tag to a target name.
func tagName(name, tag string) string {
	if name[0] != '_' {
		name = "_" + name
	}
	if strings.ContainsRune(name, '#') {
		name = name + "_"
	} else {
		name = name + "#"
	}
	return name + tag
}

func subinclude(s *scope, args []pyObject) pyObject {
	l := subincludeLabel(s, args)
	t := s.state.Graph.Target(l)
	if t == nil || t.State() < core.Built {
		// The target is not yet built. Defer parsing it until it is.
		panic(errDeferParse{Label: l})
	} else if l.PackageName != subincludePackageName && s.pkg != nil {
		s.pkg.RegisterSubinclude(l)
	}
	for _, out := range t.Outputs() {
		s.SetAll(s.interpreter.Subinclude(path.Join(t.OutDir(), out)), false)
	}
	return None
}

// subincludeLabel returns the label for a subinclude() call (which might be indirect
// if the given argument was a URL instead of a build label)
func subincludeLabel(s *scope, args []pyObject) core.BuildLabel {
	target := string(args[0].(pyString))
	s.NAssert(strings.HasPrefix(target, ":"), "Subincludes cannot be from the local package")
	if !strings.HasPrefix(target, "http") {
		return core.ParseBuildLabel(target, "")
	}
	// Check if this target is already registered (this will always happen eventually because
	// we re-parse the same package again).
	name := strings.Replace(path.Base(target), ".", "_", -1)
	label := core.NewBuildLabel(subincludePackageName, name)
	if s.state.Graph.Target(label) != nil {
		return label
	}
	remoteFile, ok := s.interpreter.subincludeScope.Lookup("remote_file").(*pyFunc)
	s.interpreter.subincludeScope.Assert(ok, "remote_file is not callable")
	// Call using the normal entry point, which is a bit of a faff but it sorts out default arguments and so forth
	a := []CallArgument{
		{
			Expr:  &Expression{Ident: &IdentExpr{Name: "name"}},
			Value: &Expression{String: `"` + name + `"`},
		}, {
			Expr:  &Expression{Ident: &IdentExpr{Name: "url"}},
			Value: &Expression{String: `"` + target + `"`},
		},
	}
	if args[1] != nil && args[1] != None {
		a = append(a, CallArgument{
			Expr: &Expression{Ident: &IdentExpr{Name: "hashes"}},
			Value: &Expression{List: &List{
				Values: []*Expression{{
					String: `"` + string(args[1].(pyString)) + `"`,
				}},
			}},
		})
	}
	remoteFile.Call(s.interpreter.subincludeScope, &Call{Arguments: a})
	return label
}

func lenFunc(s *scope, args []pyObject) pyObject {
	return pyInt(args[0].Len())
}

func isinstance(s *scope, args []pyObject) pyObject {
	obj := args[0]
	types := args[1]
	if f, ok := types.(*pyFunc); ok && isType(obj, f.name) {
		// Special case for 'str' and so forth that are functions but also types.
		return True
	} else if l, ok := types.(pyList); ok {
		for _, li := range l {
			if lif, ok := li.(*pyFunc); ok && isType(obj, lif.name) {
				return True
			} else if reflect.TypeOf(obj) == reflect.TypeOf(li) {
				return True
			}
		}
	}
	return newPyBool(reflect.TypeOf(obj) == reflect.TypeOf(types))
}

func isType(obj pyObject, name string) bool {
	switch obj.(type) {
	case pyBool:
		return name == "bool" || name == "int" // N.B. For compatibility with old assert statements
	case pyInt:
		return name == "int"
	case pyString:
		return name == "str"
	case pyList:
		return name == "list"
	case pyDict:
		return name == "dict"
	}
	return false
}

func callable(s *scope, args []pyObject) pyObject {
	_, ok := args[0].(*pyFunc)
	return newPyBool(ok)
}

func strJoin(s *scope, args []pyObject) pyObject {
	self := string(args[0].(pyString))
	seq := asStringList(s, args[1], "seq")
	return pyString(strings.Join(seq, self))
}

func strSplit(s *scope, args []pyObject) pyObject {
	self := args[0].(pyString)
	on := args[1].(pyString)
	return fromStringList(strings.Split(string(self), string(on)))
}

func strReplace(s *scope, args []pyObject) pyObject {
	self := args[0].(pyString)
	old := args[1].(pyString)
	new := args[2].(pyString)
	return pyString(strings.Replace(string(self), string(old), string(new), -1))
}

func strPartition(s *scope, args []pyObject) pyObject {
	self := args[0].(pyString)
	sep := args[1].(pyString)
	if idx := strings.Index(string(self), string(sep)); idx != -1 {
		return pyList{self[:idx], self[idx : idx+1], self[idx+1:]}
	}
	return pyList{self, pyString(""), pyString("")}
}

func strRPartition(s *scope, args []pyObject) pyObject {
	self := args[0].(pyString)
	sep := args[1].(pyString)
	if idx := strings.LastIndex(string(self), string(sep)); idx != -1 {
		return pyList{self[:idx], self[idx : idx+1], self[idx+1:]}
	}
	return pyList{pyString(""), pyString(""), self}
}

func strStartsWith(s *scope, args []pyObject) pyObject {
	self := args[0].(pyString)
	x := args[1].(pyString)
	return newPyBool(strings.HasPrefix(string(self), string(x)))
}

func strEndsWith(s *scope, args []pyObject) pyObject {
	self := args[0].(pyString)
	x := args[1].(pyString)
	return newPyBool(strings.HasSuffix(string(self), string(x)))
}

func strLStrip(s *scope, args []pyObject) pyObject {
	self := args[0].(pyString)
	cutset := args[1].(pyString)
	return pyString(strings.TrimLeft(string(self), string(cutset)))
}

func strRStrip(s *scope, args []pyObject) pyObject {
	self := args[0].(pyString)
	cutset := args[1].(pyString)
	return pyString(strings.TrimRight(string(self), string(cutset)))
}

func strFind(s *scope, args []pyObject) pyObject {
	self := args[0].(pyString)
	needle := args[1].(pyString)
	return pyInt(strings.Index(string(self), string(needle)))
}

func strRFind(s *scope, args []pyObject) pyObject {
	self := args[0].(pyString)
	needle := args[1].(pyString)
	return pyInt(strings.LastIndex(string(self), string(needle)))
}

func strFormat(s *scope, args []pyObject) pyObject {
	self := string(args[0].(pyString))
	for k, v := range s.locals {
		self = strings.Replace(self, "{"+k+"}", v.String(), -1)
	}
	return pyString(strings.Replace(strings.Replace(self, "{{", "{", -1), "}}", "}", -1))
}

func strCount(s *scope, args []pyObject) pyObject {
	self := string(args[0].(pyString))
	needle := string(args[1].(pyString))
	return pyInt(strings.Count(self, needle))
}

func boolType(s *scope, args []pyObject) pyObject {
	return newPyBool(s.Lookup("b").IsTruthy())
}

func strType(s *scope, args []pyObject) pyObject {
	return pyString(args[0].String())
}

func glob(s *scope, args []pyObject) pyObject {
	includes := asStringList(s, args[0], "includes")
	var excludes []string
	if args[1] != nil {
		excludes = asStringList(s, args[1], "excludes")
	} else if args[2] != nil {
		// TODO(peterebden): remove excludes in favour of exclude, since Buck prefers the latter now and Bazel requires it.
		excludes = asStringList(s, args[2], "exclude")
	}
	hidden := args[3].IsTruthy()
	excludes = append(excludes, s.state.Config.Parse.BuildFileName...)
	return fromStringList(core.Glob(s.pkg.Name, includes, excludes, excludes, hidden))
}

func asStringList(s *scope, arg pyObject, name string) []string {
	l, ok := arg.(pyList)
	s.Assert(ok, "argument %s must be a list", name)
	sl := make([]string, len(l))
	for i, x := range l {
		sx, ok := x.(pyString)
		s.Assert(ok, "%s must be a list of strings", name)
		sl[i] = string(sx)
	}
	return sl
}

func fromStringList(l []string) pyList {
	ret := make(pyList, len(l))
	for i, s := range l {
		ret[i] = pyString(s)
	}
	return ret
}

func configGet(s *scope, args []pyObject) pyObject {
	self := args[0].(*pyConfig)
	return self.Get(string(args[1].(pyString)), args[2])
}

func dictGet(s *scope, args []pyObject) pyObject {
	self := args[0].(pyDict)
	sk, ok := args[1].(pyString)
	s.Assert(ok, "dict keys must be strings, not %s", args[1].Type())
	if ret, present := self[string(sk)]; present {
		return ret
	}
	return args[2]
}

func dictKeys(s *scope, args []pyObject) pyObject {
	self := args[0].(pyDict)
	ret := make(pyList, 0, len(self))
	for k := range self {
		ret = append(ret, pyString(k))
	}
	return ret
}

func dictValues(s *scope, args []pyObject) pyObject {
	self := args[0].(pyDict)
	ret := make(pyList, 0, len(self))
	for _, v := range self {
		ret = append(ret, v)
	}
	return ret
}

func dictItems(s *scope, args []pyObject) pyObject {
	self := args[0].(pyDict)
	ret := make(pyList, 0, len(self))
	for k, v := range self {
		ret = append(ret, pyList{pyString(k), v})
	}
	return ret
}

func dictCopy(s *scope, args []pyObject) pyObject {
	self := args[0].(pyDict)
	ret := make(pyDict, len(self))
	for k, v := range self {
		ret[k] = v
	}
	return ret
}

func sorted(s *scope, args []pyObject) pyObject {
	l, ok := args[0].(pyList)
	s.Assert(ok, "unsortable type %s", args[0].Type())
	l = l[:]
	sort.Slice(l, func(i, j int) bool { return l[i].Operator(LessThan, l[j]).IsTruthy() })
	return l
}

func joinPath(s *scope, args []pyObject) pyObject {
	l := make([]string, len(args))
	for i, arg := range args {
		l[i] = string(arg.(pyString))
	}
	return pyString(path.Join(l...))
}

func packageName(s *scope, args []pyObject) pyObject {
	return pyString(s.pkg.Name)
}

func pyRange(s *scope, args []pyObject) pyObject {
	start, ok := args[0].(pyInt)
	stop, ok2 := args[1].(pyInt)
	if _, isBool := args[1].(pyBool); isBool {
		// Stop not passed so we start at 0 and start is the stop.
		stop = start
		start = 0
		ok2 = true
	}
	s.Assert(ok && ok2, "Arguments to range() must be integers")
	ret := make(pyList, stop-start)
	for i := start; i < stop; i++ {
		ret[i-start] = i
	}
	return ret
}

func enumerate(s *scope, args []pyObject) pyObject {
	l, ok := args[0].(pyList)
	s.Assert(ok, "Argument to enumerate must be a list, not %s", args[0].Type())
	ret := make(pyList, len(l))
	for i, li := range l {
		ret[i] = pyList{pyInt(i), li}
	}
	return ret
}

func zip(s *scope, args []pyObject) pyObject {
	lastLen := 0
	for i, seq := range args {
		si, ok := seq.(pyList)
		s.Assert(ok, "Arguments to zip must be lists, not %s", si.Type())
		// This isn't a restriction in Python but I can't be bothered handling all the stuff that real zip does.
		s.Assert(i == 0 || lastLen == len(si), "All arguments to zip must have the same length")
		lastLen = len(si)
	}
	ret := make(pyList, lastLen)
	for i := range ret {
		r := make(pyList, len(args))
		for j, li := range args {
			r[j] = li.(pyList)[i]
		}
		ret[i] = r
	}
	return ret
}

// getLabels returns the set of labels for a build target and its transitive dependencies.
// The labels are filtered by the given prefix, which is stripped from the returned labels.
// Two formats are supported here: either passing just the name of a target in the current
// package, or a build label referring specifically to one.
func getLabels(s *scope, args []pyObject) pyObject {
	name := string(args[0].(pyString))
	prefix := string(args[1].(pyString))
	if core.LooksLikeABuildLabel(name) {
		label := core.ParseBuildLabel(name, s.pkg.Name)
		return getLabelsInternal(s.state.Graph.TargetOrDie(label), prefix, core.Built)
	}
	target := getTargetPost(s, name)
	return getLabelsInternal(target, prefix, core.Building)
}

func getLabelsInternal(target *core.BuildTarget, prefix string, minState core.BuildTargetState) pyObject {
	if target.State() < minState {
		log.Fatalf("get_labels called on a target that is not yet built: %s", target.Label)
	}
	labels := map[string]bool{}
	done := map[*core.BuildTarget]bool{}
	var getLabels func(*core.BuildTarget)
	getLabels = func(t *core.BuildTarget) {
		for _, label := range t.Labels {
			if strings.HasPrefix(label, prefix) {
				labels[strings.TrimSpace(strings.TrimPrefix(label, prefix))] = true
			}
		}
		done[t] = true
		if !t.OutputIsComplete || t == target {
			for _, dep := range t.Dependencies() {
				if !done[dep] {
					getLabels(dep)
				}
			}
		}
	}
	getLabels(target)
	ret := make([]string, len(labels))
	i := 0
	for label := range labels {
		ret[i] = label
		i++
	}
	sort.Strings(ret)
	return fromStringList(ret)
}

// getTargetPost is called by various functions to get a target from the current package.
// Panics if the target is not in the current package or has already been built.
func getTargetPost(s *scope, name string) *core.BuildTarget {
	target := s.pkg.Target(name)
	s.Assert(target != nil, "Unknown build target %s in %s", name, s.pkg.Name)
	// It'd be cheating to try to modify targets that're already built.
	// Prohibit this because it'd likely end up with nasty race conditions.
	s.Assert(target.State() < core.Built, "Attempted to modify target %s, but it's already built", target.Label)
	return target
}

// addDep adds a dependency to a target.
func addDep(s *scope, args []pyObject) pyObject {
	s.Assert(s.Callback, "can only be called from a pre- or post-build callback")
	target := getTargetPost(s, string(args[0].(pyString)))
	dep := core.ParseBuildLabel(string(args[1].(pyString)), s.pkg.Name)
	exported := args[2].IsTruthy()
	target.AddMaybeExportedDependency(dep, exported, false)
	// Note that here we're in a post-build function so we must call this explicitly
	// (in other callbacks it's handled after the package parses all at once).
	s.state.Graph.AddDependency(target.Label, dep)
	s.pkg.MarkTargetModified(target)
	return None
}

// addOut adds an output to a target.
func addOut(s *scope, args []pyObject) pyObject {
	target := getTargetPost(s, string(args[0].(pyString)))
	name := string(args[1].(pyString))
	out := string(args[2].(pyString))
	if out == "" {
		target.AddOutput(name)
		s.pkg.MustRegisterOutput(name, target)
	} else {
		target.AddNamedOutput(name, out)
		s.pkg.MustRegisterOutput(out, target)
	}
	return None
}

// addLicence adds a licence to a target.
func addLicence(s *scope, args []pyObject) pyObject {
	target := getTargetPost(s, string(args[0].(pyString)))
	target.AddLicence(string(args[1].(pyString)))
	return None
}

// getCommand gets the command of a target, optionally for a configuration.
func getCommand(s *scope, args []pyObject) pyObject {
	target := getTargetPost(s, string(args[0].(pyString)))
	return pyString(target.GetCommandConfig(string(args[1].(pyString))))
}

// setCommand sets the command of a target, optionally for a configuration.
func setCommand(s *scope, args []pyObject) pyObject {
	target := getTargetPost(s, string(args[0].(pyString)))
	config := string(args[1].(pyString))
	command := string(args[2].(pyString))
	if command == "" {
		target.Command = config
	} else {
		target.AddCommand(config, command)
	}
	return None
}