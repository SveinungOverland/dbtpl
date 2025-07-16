// ackage cmd provides dbtpl command-line application logic.
package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/kenshaw/glob"
	"github.com/kenshaw/snaker"
	"github.com/SveinungOverland/dbtpl/loader"
	"github.com/SveinungOverland/dbtpl/models"
	"github.com/SveinungOverland/dbtpl/templates"
	xo "github.com/SveinungOverland/dbtpl/types"
	"github.com/xo/dburl"
	"github.com/xo/dburl/passfile"
	"github.com/xo/ox"
	_ "github.com/xo/ox/glob"
	"github.com/xo/ox/otx"
	"github.com/yookoala/realpath"
)

// Run runs the code generation.
func Run(ctx context.Context, name string) {
	// peek template set
	ts, err := newTemplateSet(
		ctx,
		parseArg("--src", "-d", os.Args),
		parseArg("--template", "-t", os.Args),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	// args
	args := &Args{
		TemplateTypes: ts.Targets(),
	}
	// build command
	opts, err := rootCommand(name, ts, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	ox.RunContext(ctx, opts...)
}

// Args contains command-line arguments.
type Args struct {
	// TemplateTypes are the allowed template types.
	TemplateTypes []string
	// Verbose enables verbose output.
	Verbose bool
	// LoaderParams are database loader parameters.
	LoaderParams LoaderParams
	// TemplateParams are template parameters.
	TemplateParams TemplateParams
	// QueryParams are query parameters.
	QueryParams QueryParams
	// SchemaParams are schema parameters.
	SchemaParams SchemaParams
	// OutParams are out parameters.
	OutParams OutParams
}

// LoaderParams are loader parameters.
type LoaderParams struct {
	// Schema is the name of the database schema.
	Schema string
}

// TemplateParams are template parameters.
type TemplateParams struct {
	// Type is the name of the template.
	Type string
	// TypeChanged is the changed flag for type.
	TypeChanged bool
	// Src is the src directory of the template.
	Src string
	// SrcChanged is the changed flag for src.
	SrcChanged bool
}

// QueryParams are query parameters.
type QueryParams struct {
	// Query is the query to introspect.
	Query string
	// Type is the type name.
	Type string
	// TypeComment is the type comment.
	TypeComment string
	// Func is the func name.
	Func string
	// FuncComment is the func comment.
	FuncComment string
	// Trim enables triming whitespace.
	Trim bool
	// Strip enables stripping the '::<type> AS <name>' in queries.
	Strip bool
	// One toggles the generated code to expect only one result.
	One bool
	// Flat toggles the generated code to return all scanned values directly.
	Flat bool
	// Exec toggles the generated code to do a db exec.
	Exec bool
	// Interpolate enables interpolation.
	Interpolate bool
	// Delimiter is the delimiter for parameterized values.
	Delimiter string
	// Fields are the fields to scan the result to.
	Fields string
	// AllowNulls enables results to have null types.
	AllowNulls bool
}

// SchemaParams are schema parameters.
type SchemaParams struct {
	// FkMode is the foreign resolution mode.
	FkMode string
	// Include allows the user to specify which types should be included. Can
	// match multiple types via regex patterns.
	//
	// - When unspecified, all types are included.
	// - When specified, only types match will be included.
	// - When a type matches an exclude entry and an include entry,
	//   the exclude entry will take precedence.
	Include []*glob.Glob
	// Exclude allows the user to specify which types should be skipped. Can
	// match multiple types via regex patterns.
	//
	// When unspecified, all types are included in the schema.
	Exclude []*glob.Glob
	// UseIndexNames toggles using index names.
	//
	// This is not enabled by default, because index names are often generated
	// using database design software which often gives non-descriptive names
	// to indexes (for example, 'authors__b124214__u_idx' instead of the more
	// descriptive 'authors_title_idx').
	UseIndexNames bool
}

// OutParams are out parameters.
type OutParams struct {
	// Out is the out path.
	Out string
	// Append toggles appending to existing file.
	Append bool
	// Single when true changes behavior so that output is to one file.
	Single string
	// Debug toggles direct writing of files to disk, skipping post processing.
	Debug bool
}

// newTemplateSet creates a new templates set.
func newTemplateSet(ctx context.Context, dir, template string) (*templates.Templates, error) {
	// build template ts
	ts := templates.NewDefaults(ctx)
	switch {
	case dir == "" && template == "":
		// show all default templates
		if err := ts.LoadDefaults(ctx); err != nil {
			return nil, err
		}
	case template != "":
		// only load the selected default template
		if err := ts.LoadDefault(ctx, template); err != nil {
			return nil, err
		}
		ts.Use(template)
	default:
		// load specified template
		s := snaker.SnakeToCamel(filepath.Base(dir))
		s = strings.ReplaceAll(strings.ToLower(s), "_", "-")
		// add template
		var err error
		if s, err = ts.Add(ctx, s, os.DirFS(dir), false); err != nil {
			return nil, err
		}
		// use
		ts.Use(s)
	}
	return ts, nil
}

// rootCommand creates the root command.
func rootCommand(name string, ts *templates.Templates, args *Args) ([]ox.Option, error) {
	// root
	opts := []ox.Option{
		ox.Usage(name, "the templated code generator for databases."),
		ox.Defaults(),
		ox.Flags().
			Bool(
				"verbose", "enable verbose output",
				ox.Bind(&args.Verbose),
				ox.Short("v"),
			),
	}
	// add sub commands
	for _, f := range []func(*templates.Templates, *Args) ([]ox.Option, error){
		queryCommand,
		schemaCommand,
		dumpCommand,
	} {
		subopts, err := f(ts, args)
		if err != nil {
			return nil, err
		}
		opts = append(opts, ox.Sub(subopts...))
	}
	return opts, nil
}

// queryCommand builds the query command options.
func queryCommand(ts *templates.Templates, args *Args) ([]ox.Option, error) {
	// query flags
	fs := ox.Flags()
	fs = databaseFlags(fs, args)
	fs = outFlags(fs, args)
	fs = fs.
		String(
			"query", "custom database query (uses stdin if not provided)",
			ox.Bind(&args.QueryParams.Query),
			ox.Short("Q"),
		).
		String(
			"type", "type name",
			ox.Bind(&args.QueryParams.Type),
			ox.Short("T"),
		).
		String(
			"type-comment", "type comment",
			ox.Bind(&args.QueryParams.TypeComment),
		).
		String(
			"func", "func name",
			ox.Bind(&args.QueryParams.Func),
			ox.Short("F")).
		String(
			"func-comment", "func comment",
			ox.Bind(&args.QueryParams.FuncComment),
		).
		Bool(
			"trim", "enable trimming whitespace",
			ox.Bind(&args.QueryParams.Trim),
			ox.Short("M"),
		).
		Bool(
			"strip", "enable stripping type casts",
			ox.Bind(&args.QueryParams.Strip),
			ox.Short("B"),
		).
		Bool(
			"one", "enable returning single (only one) result",
			ox.Bind(&args.QueryParams.One),
			ox.Short("1"),
		).
		Bool(
			"flat", "enable returning unstructured (flat) values",
			ox.Bind(&args.QueryParams.Flat),
			ox.Short("l"),
		).
		Bool(
			"exec", "enable exec (disables query introspection)",
			ox.Bind(&args.QueryParams.Exec),
			ox.Short("X"),
		).
		Bool(
			"interpolate", "enable interpolation of embedded params",
			ox.Bind(&args.QueryParams.Interpolate),
			ox.Short("I"),
		).
		String(
			"delimiter", "delimiter used for embedded params",
			ox.Bind(&args.QueryParams.Delimiter),
			ox.Short("L"),
			ox.Default("%%"),
		).
		String(
			"fields", "override field names for results",
			ox.Bind(&args.QueryParams.Fields),
			ox.Short("Z"),
		).
		Bool(
			"allow-nulls", "allow result fields with NULL values",
			ox.Bind(&args.QueryParams.AllowNulls),
			ox.Short("U"),
		)
	var err error
	if fs, err = addFlags(fs, ts, args, true, false); err != nil {
		return nil, err
	}
	return []ox.Option{
		ox.Usage("query", "generate code for a database query from a template"),
		ox.Banner("Generate code for a database query from a template."),
		ox.Spec("[flags] <database url>"),
		ox.ValidArgs(1, 1),
		fs,
		ox.Exec(exec("query", ts, args)),
	}, nil
}

// schemaCommand builds the schema command options.
func schemaCommand(ts *templates.Templates, args *Args) ([]ox.Option, error) {
	// schema flags
	fs := ox.Flags()
	fs = databaseFlags(fs, args)
	fs = outFlags(fs, args)
	fs = fs.
		String(
			"fk-mode", "foreign key resolution mode",
			ox.Default("smart"),
			ox.Bind(&args.SchemaParams.FkMode),
			ox.Short("k"),
			ox.Valid("smart", "parent", "field", "key"),
		).
		Slice(
			"include", "include types",
			ox.Bind(&args.SchemaParams.Include),
			ox.Elem(ox.GlobT),
			ox.Short("i"),
		).
		Slice(
			"exclude", "exclude types",
			ox.Bind(&args.SchemaParams.Exclude),
			ox.Short("e"),
			ox.Elem(ox.GlobT),
		).
		Bool(
			"use-index-names", "use index names as defined in schema for generated code",
			ox.Bind(&args.SchemaParams.UseIndexNames),
			ox.Short("j"),
		)
	var err error
	if fs, err = addFlags(fs, ts, args, true, true); err != nil {
		return nil, err
	}
	return []ox.Option{
		ox.Usage("schema", "generate code for a database schema from a template"),
		ox.Banner("Generate code for a database schema from a template."),
		ox.Spec("[flags] <database url>"),
		ox.ValidArgs(1, 1),
		fs,
		ox.Exec(exec("schema", ts, args)),
	}, nil
}

// dumpCommand builds the dump command options.
func dumpCommand(ts *templates.Templates, args *Args) ([]ox.Option, error) {
	fs, err := addFlags(ox.Flags(), ts, args, false, false)
	if err != nil {
		return nil, err
	}
	return []ox.Option{
		ox.Usage("dump", "dump template to path"),
		ox.Banner("Dump template to path."),
		ox.Spec("[flags] <out dir>"),
		ox.ValidArgs(1, 1),
		fs,
		ox.Exec(func(ctx context.Context, v []string) error {
			// set template
			ts.Use(args.TemplateParams.Type)
			// get template src
			src, err := ts.Src()
			if err != nil {
				return err
			}
			// ensure out dir exists
			if err := checkDir(v[0]); err != nil {
				return err
			}
			// dump
			return iofs.WalkDir(src, ".", func(n string, d iofs.DirEntry, err error) error {
				switch {
				case err != nil:
					return err
				case d.IsDir():
					return os.MkdirAll(filepath.Join(v[0], n), 0o755)
				}
				buf, err := iofs.ReadFile(src, n)
				if err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(v[0], n), buf, 0o644)
			})
		}),
	}, nil
}

// exec creates a exec func for the mode (schema/query).
func exec(mode string, ts *templates.Templates, args *Args) func(context.Context, []string) error {
	return func(ctx context.Context, cmdargs []string) error {
		// check args
		if err := checkArgs(mode, ts, args); err != nil {
			return err
		}
		// set template
		ts.Use(args.TemplateParams.Type)
		// build context
		ctx = buildContext(ctx, mode, ts, args)
		// enable verbose output for sql queries
		if args.Verbose {
			models.SetLogger(func(str string, v ...any) {
				s, z := "SQL: %s\n", []any{str}
				if len(v) != 0 {
					s, z = s+"PARAMS: %v\n", append(z, v)
				}
				fmt.Printf(s+"\n", z...)
			})
		}
		// open database
		var err error
		if ctx, err = open(ctx, cmdargs[0], args.LoaderParams.Schema); err != nil {
			return err
		}
		// load
		set, err := load(ctx, mode, ts, args)
		if err != nil {
			return err
		}
		return generate(ctx, mode, ts, set, args)
	}
}

// generate generates the dbtpl files with the provided templates, data, and
// arguments.
func generate(ctx context.Context, mode string, ts *templates.Templates, set *xo.Set, args *Args) error {
	// create set context
	ctx = ts.NewContext(ctx, mode)
	if err := displayErrors(ts); err != nil {
		return err
	}
	// preprocess
	ts.Pre(ctx, args.OutParams.Out, mode, set)
	if err := displayErrors(ts); err != nil {
		return err
	}
	// process
	ts.Process(ctx, args.OutParams.Out, mode, set)
	if err := displayErrors(ts); err != nil {
		return err
	}
	// post
	if !args.OutParams.Debug {
		ts.Post(ctx, mode)
		if err := displayErrors(ts); err != nil {
			return err
		}
	}
	// dump
	ts.Dump(args.OutParams.Out)
	if err := displayErrors(ts); err != nil {
		return err
	}
	return nil
}

// databaseFlags adds database flags to the flag set.
func databaseFlags(fs *ox.FlagSet, args *Args) *ox.FlagSet {
	return fs.
		String(
			"schema", "database schema name",
			ox.Bind(&args.LoaderParams.Schema),
			ox.Short("s"),
		)
}

// outFlags adds out flags to the flag set.
func outFlags(fs *ox.FlagSet, args *Args) *ox.FlagSet {
	return fs.
		String(
			"out", "out path",
			ox.Bind(&args.OutParams.Out),
			ox.Short("o"),
			ox.Default("models"),
		).
		Bool(
			"append", "enable append mode (dependent on template support)",
			ox.Bind(&args.OutParams.Append),
			ox.Short("a"),
		).
		String(
			"single", "output all contents to the specified file",
			ox.Bind(&args.OutParams.Single),
			ox.Short("S"),
		).
		Bool(
			"debug", "debug generated code (writes generated code to disk without post processing)",
			ox.Bind(&args.OutParams.Debug),
			ox.Short("D"),
		)
}

// addFlags adds template flags to the flag set.
func addFlags(fs *ox.FlagSet, ts *templates.Templates, args *Args, extraFlags, loaderFlags bool) (*ox.FlagSet, error) {
	fs = fs.
		Var(
			"template", "template type",
			ox.BindSet(&args.TemplateParams.Type, &args.TemplateParams.TypeChanged),
			ox.Short("t"),
			ox.Default(ts.Target()),
			ox.Valid(args.TemplateTypes...),
		)
	var flags []xo.FlagSet
	if extraFlags {
		fs = fs.
			String(
				"src", "template source directory",
				ox.BindSet(&args.TemplateParams.Src, &args.TemplateParams.SrcChanged),
				ox.Short("d"),
			)
		for _, name := range ts.Targets() {
			flags = append(flags, ts.Flags(name)...)
		}
	}
	if loaderFlags {
		flags = append(flags, loader.Flags()...)
	}
	var err error
	for _, g := range flags {
		if fs, err = addFlag(fs, g); err != nil {
			return nil, err
		}
	}
	return fs, nil
}

// addFlag adds the flag to the cmd.
func addFlag(fs *ox.FlagSet, g xo.FlagSet) (*ox.FlagSet, error) {
	typ := ox.StringT
	switch g.Flag.Type {
	case "string":
	case "bool":
		typ = ox.BoolT
	case "int":
		typ = ox.IntT
	case "[]string":
		typ = ox.SliceT
	case "glob":
		typ = ox.GlobT
	default:
		return nil, fmt.Errorf("unknown flag type %s", g.Flag.Type)
	}
	opts := []ox.Option{typ, ox.Hidden(g.Flag.Hidden)}
	if g.Flag.Short != "" {
		opts = append(opts, ox.Short(g.Flag.Short))
	}
	switch typ {
	case ox.BoolT:
		def, noArgDef := false, true
		if g.Flag.Default != nil {
			def, _ = g.Flag.Default.(bool)
			noArgDef = !def
		}
		opts = append(opts, ox.Default(def), ox.NoArg(true, noArgDef))
	case ox.IntT:
		def := 0
		if g.Flag.Default != nil {
			def, _ = g.Flag.Default.(int)
		}
		opts = append(opts, ox.Default(def))
	case ox.SliceT:
		def := ""
		if g.Flag.Default != nil {
			switch x := g.Flag.Default.(type) {
			case string:
				def = x
			case []string:
				def = strings.Join(x, ",")
			}
		}
		opts = append(opts, ox.Default(def))
	default:
		def := ""
		if g.Flag.Default != nil {
			def, _ = g.Flag.Default.(string)
		}
		if g.Flag.Enums != nil && def == "" {
			def = g.Flag.Enums[0]
		}
		opts = append(opts, ox.Default(def))
	}
	if len(g.Flag.Aliases) != 0 {
		opts = append(opts, ox.Aliases(g.Flag.Aliases...))
	}
	desc := g.Flag.Desc
	if g.Flag.Enums != nil {
		desc += " <" + strings.Join(g.Flag.Enums, "|") + ">"
		opts = append(opts, ox.Valid(g.Flag.Enums...))
	}
	return fs.Var(
		g.Key(), desc,
		opts...,
	), nil
}

// checkArgs sets up and checks args.
func checkArgs(mode string, ts *templates.Templates, args *Args) error {
	// check template is available for the mode
	if err := ts.For(mode); err != nil {
		return err
	}
	// check --src and --template are exclusive
	if args.TemplateParams.SrcChanged && args.TemplateParams.TypeChanged {
		return errors.New("--src and --template cannot be used together")
	}
	// read query string from stdin if not provided via --query
	if mode == "query" && args.QueryParams.Query == "" {
		buf, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		args.QueryParams.Query = string(bytes.TrimRight(buf, "\r\n"))
	}
	// check out path
	if args.OutParams.Out != "" {
		var err error
		if args.OutParams.Out, err = realpath.Realpath(args.OutParams.Out); err != nil {
			return err
		}
		if err := checkDir(args.OutParams.Out); err != nil {
			return err
		}
	}
	return nil
}

// buildContext builds a context for the mode and template.
func buildContext(ctx context.Context, mode string, ts *templates.Templates, args *Args) context.Context {
	// out params
	ctx = context.WithValue(ctx, xo.OutKey, args.OutParams.Out)
	ctx = context.WithValue(ctx, xo.AppendKey, args.OutParams.Append)
	ctx = context.WithValue(ctx, xo.SingleKey, args.OutParams.Single)
	// add flags
	flags := ts.Flags(args.TemplateParams.Type)
	if mode == "schema" {
		flags = append(flags, loader.Flags()...)
	}
	for _, g := range flags {
		v, ok := otx.Any(ctx, g.Key())
		if !ok {
			panic(fmt.Sprintf("param %q was not defined in context vars", g.Key()))
		}
		var z any
		if g.Flag.Type != "[]string" {
			z = v.Val()
		} else {
			a, ok := v.Val().([]ox.Value)
			if !ok {
				panic(fmt.Sprintf("param %q was not []ox.Value in context vars", g.Key()))
			}
			s := make([]string, len(a))
			for i, v := range a {
				if s[i], ok = v.Val().(string); !ok {
					panic(fmt.Sprintf("param %q element %d was not string in context vars", g.Key(), i))
				}
			}
			z = s
		}
		ctx = context.WithValue(ctx, g.Flag.ContextKey, z)
	}
	return ctx
}

// open opens a connection to the database, returning a context for use in
// template generation.
func open(ctx context.Context, urlstr, schema string) (context.Context, error) {
	v, err := user.Current()
	if err != nil {
		return nil, err
	}
	// parse dsn
	u, err := dburl.Parse(urlstr)
	if err != nil {
		return nil, err
	}
	// open database
	db, err := passfile.OpenURL(u, v.HomeDir, "dbtplpass")
	if err != nil {
		return nil, err
	}
	// add driver, db, schema to context
	ctx = context.WithValue(ctx, xo.DriverKey, u.Driver)
	ctx = context.WithValue(ctx, xo.DbKey, db)
	// determine schema
	if schema == "" {
		if schema, err = loader.Schema(ctx); err != nil {
			return nil, err
		}
	}
	ctx = context.WithValue(ctx, xo.SchemaKey, schema)
	return ctx, nil
}

// load loads a set of queries or schemas.
func load(ctx context.Context, mode string, _ *templates.Templates, args *Args) (*xo.Set, error) {
	f := loadSchema
	if mode == "query" {
		f = loadQuery
	}
	set := new(xo.Set)
	if err := f(ctx, set, args); err != nil {
		return nil, err
	}
	return set, nil
}

// parseArg peeks at a flag in args.
func parseArg(full, short string, args []string) (s string) {
	defer func() {
		s = strings.TrimSpace(s)
	}()
	for i := range args {
		switch s := strings.TrimSpace(args[i]); {
		case s == short, s == full:
			if i < len(args)-1 {
				return args[i+1]
			}
		case strings.HasPrefix(s, short):
			return strings.TrimPrefix(s, short)
		case strings.HasPrefix(s, full):
			return strings.TrimPrefix(s, full)
		}
	}
	return ""
}

// displayErrors displays collected errors from the set.
func displayErrors(ts *templates.Templates) error {
	if errors := ts.Errors(); len(errors) != 0 {
		for _, err := range errors {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		return fmt.Errorf("%d errors encountered", len(errors))
	}
	return nil
}

// checkDir checks that dir exists.
func checkDir(dir string) error {
	if !isDir(dir) {
		return fmt.Errorf("%s must exist and must be a directory", dir)
	}
	return nil
}

// isDir determines if dir is a directory.
func isDir(dir string) bool {
	if fi, err := os.Stat(dir); err == nil {
		return fi.IsDir()
	}
	return false
}
