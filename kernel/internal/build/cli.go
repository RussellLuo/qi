package build

import (
	"context"
	"fmt"
	"io"
	"strings"

	builder "github.com/RussellLuo/gordis/build"
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	legacy := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}

		if arg == "-h" || arg == "--help" {
			return builder.Run(ctx, []string{"--help"}, stdout, stderr, qiCommand())
		}
		flag, _, supplied := strings.Cut(arg, "=")
		switch flag {
		case "--source", "-source", "--manifest", "-manifest":
			legacy = true
		}
		switch flag {
		case "--with", "-with", "--replace", "-replace", "--source", "-source", "--manifest", "-manifest", "--output", "-output":
			if !supplied {
				i++ // Legacy flag values named --help are data too.
			}
		}
	}
	if !legacy {
		return builder.Run(ctx, args, stdout, stderr, qiCommand())
	}
	// Only legacy flags use the compatibility parser. Both paths use Build.
	options, err := parseLegacyCLI(args)
	if err != nil {
		fmt.Fprintln(stderr, "qi-build:", err)
		return 2
	}
	recordPath, err := Build(ctx, options)
	if err != nil {
		fmt.Fprintln(stderr, "qi-build:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Built %s\nBuild record: %s\n", options.Output, recordPath)
	return 0
}

func qiCommand() builder.Command {
	return builder.Command{
		Name: "qi-build", DefaultOutput: "qi", Prepare: prepareCLI,
		Help: `Qi: qi-build [<qi_version>] [--with <package[@version][=replacement]> ...]
                [--replace <module[@version]=replacement> ...] [--output <file>]
       qi-build --manifest <build.yaml> --output <file> [--source <module>=<directory> ...]

Select package Extension() factories, at module roots or subpackages. Packages in
one module share a version and source; --replace keys remain module paths.
Queries support latest, tags, pseudo-versions,
branches and commits through Go. Remote replacements use module[@query]; local
directories resolve from the caller working directory. Fixed local versions need no download.
Core query: kernel --with > positional version > QI_VERSION > latest.
Default kernel and builtin modules resolve that query independently; generated
applications compose builtin/bundle once without an extra --with.
Unspecified queries with local sources use a development placeholder, without
querying the module remotely. --replace never selects an Extension.
Default output: ./qi (./qi.exe on Windows); records: <output>.build.
Existing outputs are refused. Legacy manifest keeps its strict fixed versions.
-h, --help: show this help without accessing sources or network.`,
	}
}
