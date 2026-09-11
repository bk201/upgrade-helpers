package precheck

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"sigs.k8s.io/yaml"
)

type NodeEnvelope struct {
	Node    string       `json:"node"`
	Results []NodeResult `json:"results"`
}

func RunLocalNodeChecks(node, root string, checks []string, now time.Time) NodeEnvelope {
	envelope := NodeEnvelope{Node: node}
	for _, check := range checks {
		switch check {
		case "certificates":
			envelope.Results = append(envelope.Results, localCertificates(node, root, now))
		case "network-config":
			envelope.Results = append(envelope.Results, localNetworkConfig(node, root))
		case "cos-state-size":
			envelope.Results = append(envelope.Results, localCOSState(node, root))
		default:
			envelope.Results = append(envelope.Results, NodeResult{Node: node, Status: Error, Summary: "unknown node check", Details: []string{check}})
		}
	}
	return envelope
}

func localCertificates(node, root string, now time.Time) NodeResult {
	directory := filepath.Join(root, "var/lib/rancher/rke2/server/tls")
	var certificateFiles []string
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".crt") {
			certificateFiles = append(certificateFiles, path)
		}
		return nil
	})
	if err != nil {
		return NodeResult{Node: node, Status: Error, Summary: "could not scan RKE2 certificates", Details: []string{err.Error()}}
	}
	if len(certificateFiles) == 0 {
		return NodeResult{Node: node, Status: Fail, Summary: "no RKE2 certificates were found", Details: []string{directory}}
	}
	sort.Strings(certificateFiles)
	var expired, expiring, parseErrors []string
	for _, path := range certificateFiles {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("%s: %v", path, readErr))
			continue
		}
		parsed := false
		for len(data) > 0 {
			block, rest := pem.Decode(data)
			if block == nil {
				break
			}
			data = rest
			if block.Type != "CERTIFICATE" {
				continue
			}
			certificate, parseErr := x509.ParseCertificate(block.Bytes)
			if parseErr != nil {
				parseErrors = append(parseErrors, fmt.Sprintf("%s: %v", path, parseErr))
				continue
			}
			parsed = true
			switch {
			case !certificate.NotAfter.After(now):
				expired = append(expired, fmt.Sprintf("%s expired at %s", path, certificate.NotAfter.UTC().Format(time.RFC3339)))
			case !certificate.NotAfter.After(now.Add(10 * 24 * time.Hour)):
				expiring = append(expiring, fmt.Sprintf("%s expires at %s", path, certificate.NotAfter.UTC().Format(time.RFC3339)))
			}
		}
		if !parsed {
			parseErrors = append(parseErrors, path+": no parseable certificate PEM block")
		}
	}
	if len(parseErrors) > 0 {
		return NodeResult{Node: node, Status: Error, Summary: "some RKE2 certificates could not be parsed", Details: parseErrors}
	}
	if len(expired) > 0 {
		return NodeResult{Node: node, Status: Fail, Summary: "expired RKE2 certificates were found", Details: append(expired, expiring...)}
	}
	if len(expiring) > 0 {
		return NodeResult{Node: node, Status: Warning, Summary: "RKE2 certificates expire within ten days", Details: expiring}
	}
	return NodeResult{Node: node, Status: Pass, Summary: "all RKE2 certificates are valid for more than ten days"}
}

func localNetworkConfig(node, root string) NodeResult {
	oem := filepath.Join(root, "oem")
	custom := filepath.Join(oem, "90_custom.yaml")
	legacy := filepath.Join(oem, "99_custom.yaml")
	configPath := filepath.Join(oem, "harvester.config")
	var failures []string
	if _, err := os.Stat(custom); err != nil {
		if _, legacyErr := os.Stat(legacy); legacyErr == nil {
			failures = append(failures, legacy+" must be renamed to "+custom)
		} else {
			failures = append(failures, custom+" does not exist")
		}
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		failures = append(failures, configPath+" does not exist or cannot be read")
	} else {
		var config map[string]any
		if err := yaml.Unmarshal(data, &config); err != nil {
			return NodeResult{Node: node, Status: Error, Summary: "harvester.config is invalid YAML", Details: []string{err.Error()}}
		}
		install, _ := config["install"].(map[string]any)
		if nonEmpty(install["networks"]) {
			failures = append(failures, configPath+" uses the old v1.0 install.networks schema")
		}
		if !nonEmpty(install["managementinterface"]) && !nonEmpty(install["management_interface"]) {
			failures = append(failures, configPath+" is missing management interface configuration")
		}
	}
	if len(failures) > 0 {
		return NodeResult{Node: node, Status: Fail, Summary: "network configuration is not upgrade-safe", Details: failures}
	}
	return NodeResult{Node: node, Status: Pass, Summary: "network configuration is upgrade-safe"}
}

func nonEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func localCOSState(node, root string) NodeResult {
	path := filepath.Join(root, "run/initramfs/cos-state")
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return NodeResult{Node: node, Status: Error, Summary: "could not determine COS_STATE filesystem size", Details: []string{err.Error()}}
	}
	bytes := uint64(stats.Blocks) * uint64(stats.Bsize)
	sizeGB := bytes / 1_000_000_000
	if sizeGB < 15 {
		return NodeResult{Node: node, Status: Fail, Summary: "COS_STATE is smaller than 15 GB", Details: []string{fmt.Sprintf("size=%d GB; upgrading to v1.8 may corrupt the OS image", sizeGB)}}
	}
	return NodeResult{Node: node, Status: Pass, Summary: fmt.Sprintf("COS_STATE is %d GB", sizeGB)}
}
