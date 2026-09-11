# Harvester upgrade pre-check

`harvester-precheck` is a read-only Go CLI that checks whether a Harvester v1.x
cluster is ready to upgrade. It talks directly to the Kubernetes API and does
not require `kubectl`, `jq`, `yq`, `awk`, or `openssl`.

Checks that need host files run through temporary DaemonSets. The same binary is
used by the CLI and by the validator image. The CLI removes its DaemonSets when
the run finishes, is canceled, or times out.

## Build

Choose the immutable image reference that will be available on every cluster
node, then build both artifacts with the same value:

```console
make test
make build VALIDATOR_IMAGE=registry.example.com/harvester-precheck@sha256:<digest>
make image VALIDATOR_IMAGE=registry.example.com/harvester-precheck:<tag>
```

Use `docker buildx build --platform linux/amd64,linux/arm64` with the same build
argument when publishing a multi-architecture validator image.

For a release build, publish the image, obtain its digest, and rebuild the CLI
with the digest-qualified `VALIDATOR_IMAGE`. Air-gapped installations must load
or mirror that image on every node before running the CLI. Validator pods use
`IfNotPresent` so preloaded images do not require registry access.

## Usage

Use a kubeconfig with cluster read access plus permission to create and delete
DaemonSets in `harvester-system` and read their pod logs:

```console
./harvester-precheck --kubeconfig /path/to/kubeconfig
```

The standard `KUBECONFIG` and current-context loading rules apply when the flags
are omitted. The command can run from any machine with API access; it does not
need to run on a control-plane node.

```text
Usage: harvester-precheck [options]
  -v, --verbose             print timestamped progress diagnostics
  -l, --log-file PATH       also write the final report to PATH
  -y, --yes                 overwrite an existing log without prompting
      --kubeconfig PATH     use this kubeconfig
      --context NAME        use this kubeconfig context
      --timeout DURATION    timeout for each node validator (default 5m)
      --validator-image REF override the compiled-in validator image
      --output text|json    select the report format (default text)
```

An existing log file is only overwritten after an interactive confirmation or
when `--yes` is set. Non-interactive runs must use `--yes` explicitly.

Statuses have the following meanings:

- `PASS`: the requirement is satisfied.
- `WARNING`: the check completed but found a non-blocking concern.
- `FAIL`: an upgrade-blocking condition was found.
- `SKIPPED`: the check does not apply to this cluster version/configuration.
- `ERROR`: the requirement could not be evaluated reliably.

The process exits `0` when there are no failures or errors, `1` when at least one
check fails or errors, and `2` for invalid command-line usage.

## JSON reports

`--output json` emits a stable `v1` report containing the cluster version, run
timestamps, summary counts, and ordered check results. Node-scoped checks also
contain the result from every expected node. Progress messages from `--verbose`
are written to stderr and do not corrupt JSON output.

## Node validation

The CLI creates two kinds of short-lived validator DaemonSets as needed:

- A control-plane validator checks RKE2 certificate expiration on every
  control-plane node.
- An all-node validator checks the v1.6 network configuration or the v1.7
  `COS_STATE` size when that version-specific check applies.

Validators mount only the required host directories read-only, drop all Linux
capabilities, disable privilege escalation, and do not mount a service-account
token. A result is required from every expected node; missing output, image-pull
failures, scheduling failures, and timeouts are reported as `ERROR`.

The `node-check`, `hold`, and `health` subcommands are internal container
entrypoints and are not operator-facing APIs.

Time synchronization is not inferred by these checks. When the cluster has no
configured NTP source, verify node clocks separately before upgrading.
