package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/harvester/upgrade-helpers/internal/precheck"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var defaultValidatorImage = "rancher/harvester-precheck:dev"

type options struct {
	verbose        bool
	logFile        string
	yes            bool
	kubeconfig     string
	context        string
	timeout        time.Duration
	validatorImage string
	output         string
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "node-check":
			return runNodeCheck(args[1:], stdout, stderr)
		case "hold":
			return hold()
		case "health":
			return 0
		}
	}

	var opts options
	flags := flag.NewFlagSet("harvester-precheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.BoolVar(&opts.verbose, "v", false, "enable verbose progress output")
	flags.BoolVar(&opts.verbose, "verbose", false, "enable verbose progress output")
	flags.StringVar(&opts.logFile, "l", "", "write the final report to this file")
	flags.StringVar(&opts.logFile, "log-file", "", "write the final report to this file")
	flags.BoolVar(&opts.yes, "y", false, "overwrite an existing log file without prompting")
	flags.BoolVar(&opts.yes, "yes", false, "overwrite an existing log file without prompting")
	flags.StringVar(&opts.kubeconfig, "kubeconfig", "", "path to kubeconfig (uses standard loading rules by default)")
	flags.StringVar(&opts.context, "context", "", "kubeconfig context to use")
	flags.DurationVar(&opts.timeout, "timeout", 5*time.Minute, "timeout for each node validator")
	flags.StringVar(&opts.validatorImage, "validator-image", defaultValidatorImage, "node validator image")
	flags.StringVar(&opts.output, "output", "text", "report format: text or json")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: harvester-precheck [options]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %s\n", strings.Join(flags.Args(), " "))
		return 2
	}
	if opts.output != "text" && opts.output != "json" {
		fmt.Fprintln(stderr, "--output must be text or json")
		return 2
	}
	if opts.timeout <= 0 {
		fmt.Fprintln(stderr, "--timeout must be greater than zero")
		return 2
	}
	if opts.validatorImage == "" {
		fmt.Fprintln(stderr, "--validator-image must not be empty")
		return 2
	}

	logWriter, closeLog, err := prepareLog(opts.logFile, opts.yes, stdin, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "log file: %v\n", err)
		return 2
	}
	if closeLog != nil {
		defer closeLog()
	}
	reportWriter := stdout
	if logWriter != nil {
		reportWriter = io.MultiWriter(stdout, logWriter)
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.kubeconfig != "" {
		rules.ExplicitPath = opts.kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: opts.context}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		fmt.Fprintf(stderr, "load Kubernetes configuration: %v\n", err)
		return 1
	}
	core, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(stderr, "create Kubernetes client: %v\n", err)
		return 1
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(stderr, "create dynamic Kubernetes client: %v\n", err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	env := precheck.NewEnvironment(core, dynamicClient, precheck.NewResolver(core.Discovery()))
	env.ValidatorImage = opts.validatorImage
	env.Timeout = opts.timeout
	var outputMu sync.Mutex
	env.Verbose = func(format string, values ...any) {
		if !opts.verbose {
			return
		}
		outputMu.Lock()
		defer outputMu.Unlock()
		fmt.Fprintf(stderr, "[%s] ", time.Now().Format("2006-01-02 15:04:05"))
		fmt.Fprintf(stderr, format+"\n", values...)
	}
	version, err := precheck.DiscoverVersion(ctx, env)
	if err != nil {
		fmt.Fprintf(stderr, "validate Kubernetes access and discover Harvester version: %v\n", err)
		return 1
	}
	env.Version = version
	env.Verbose("starting checks for cluster %s", version.Raw)
	report := precheck.Run(ctx, env)
	if err := precheck.WriteReport(reportWriter, report, opts.output); err != nil {
		fmt.Fprintf(stderr, "write report: %v\n", err)
		return 1
	}
	if report.Failed() {
		return 1
	}
	return 0
}

func prepareLog(path string, overwrite bool, input io.Reader, prompt io.Writer) (io.Writer, func() error, error) {
	if path == "" {
		return nil, nil, nil
	}
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return nil, nil, fmt.Errorf("%s is a directory", path)
		}
		if !overwrite {
			inputFile, ok := input.(*os.File)
			if !ok || !isTerminal(inputFile) {
				return nil, nil, fmt.Errorf("%s exists; use --yes to overwrite it in non-interactive mode", path)
			}
			fmt.Fprintf(prompt, "The file %s exists. Overwrite it? [y/N] ", path)
			var answer string
			if _, err := fmt.Fscanln(input, &answer); err != nil || (strings.ToLower(answer) != "y" && strings.ToLower(answer) != "yes") {
				return nil, nil, errors.New("overwrite declined")
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	if directory := filepath.Dir(path); directory != "." {
		if info, err := os.Stat(directory); err != nil || !info.IsDir() {
			return nil, nil, fmt.Errorf("parent directory %s does not exist", directory)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return file, file.Close, nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func runNodeCheck(args []string, output, stderr io.Writer) int {
	flags := flag.NewFlagSet("node-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var checksValue, root string
	flags.StringVar(&checksValue, "checks", "", "comma-separated node checks")
	flags.StringVar(&root, "root", "/host", "host filesystem mount root")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	node := os.Getenv("NODE_NAME")
	if node == "" || checksValue == "" {
		fmt.Fprintln(stderr, "NODE_NAME and --checks are required")
		return 2
	}
	envelope := precheck.RunLocalNodeChecks(node, root, strings.Split(checksValue, ","), time.Now())
	if err := json.NewEncoder(output).Encode(envelope); err != nil {
		fmt.Fprintf(stderr, "encode node results: %v\n", err)
		return 1
	}
	return 0
}

func hold() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	<-ctx.Done()
	return 0
}
