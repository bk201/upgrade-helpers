package precheck

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type networkConfig struct {
	Range   string   `json:"range"`
	Exclude []string `json:"exclude"`
}

type addressRange struct{ start, end uint64 }

func usableIPv4Count(include string, excludes []string) (uint64, error) {
	prefix, err := netip.ParsePrefix(include)
	if err != nil || !prefix.Addr().Is4() {
		return 0, fmt.Errorf("range %q must be a valid IPv4 CIDR", include)
	}
	bits := prefix.Bits()
	size := uint64(1) << uint(32-bits)
	if size <= 2 {
		return 0, nil
	}
	base := uint64(binary.BigEndian.Uint32(prefix.Masked().Addr().AsSlice()))
	usable := addressRange{start: base + 1, end: base + size - 2}
	ranges := make([]addressRange, 0, len(excludes))
	for _, raw := range excludes {
		excluded, parseErr := netip.ParsePrefix(raw)
		if parseErr != nil || !excluded.Addr().Is4() {
			return 0, fmt.Errorf("excluded range %q must be a valid IPv4 CIDR", raw)
		}
		excludedSize := uint64(1) << uint(32-excluded.Bits())
		excludedStart := uint64(binary.BigEndian.Uint32(excluded.Masked().Addr().AsSlice()))
		excludedEnd := excludedStart + excludedSize - 1
		if excludedStart < usable.start {
			excludedStart = usable.start
		}
		if excludedEnd > usable.end {
			excludedEnd = usable.end
		}
		if excludedStart <= excludedEnd {
			ranges = append(ranges, addressRange{excludedStart, excludedEnd})
		}
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start == ranges[j].start {
			return ranges[i].end < ranges[j].end
		}
		return ranges[i].start < ranges[j].start
	})
	count := usable.end - usable.start + 1
	for index := 0; index < len(ranges); {
		merged := ranges[index]
		index++
		for index < len(ranges) && ranges[index].start <= merged.end+1 {
			if ranges[index].end > merged.end {
				merged.end = ranges[index].end
			}
			index++
		}
		count -= merged.end - merged.start + 1
	}
	return count, nil
}

func whereaboutsPoolName(value string) string {
	if strings.HasSuffix(value, ":") {
		value += "0"
	}
	return strings.NewReplacer(":", "-", "/", "-").Replace(value)
}

func parseNetwork(value string) (networkConfig, error) {
	var config networkConfig
	if err := json.Unmarshal([]byte(value), &config); err != nil {
		return config, err
	}
	if config.Range == "" {
		return config, fmt.Errorf("network range is empty")
	}
	return config, nil
}

func availableNetworkIPs(ctx context.Context, env *Environment, name string, config networkConfig, required int64, reason string) Result {
	usable, err := usableIPv4Count(config.Range, config.Exclude)
	if err != nil {
		return NewResult(Error, name+" configuration is invalid", err.Error())
	}
	poolName := whereaboutsPoolName(config.Range)
	pool, err := env.Get(ctx, "whereabouts.cni.cncf.io", "ippools", "kube-system", poolName)
	if err != nil {
		return NewResult(Error, "could not inspect Whereabouts IPPool for "+name, err.Error())
	}
	allocated := uint64(len(objectMap(pool.Object, "spec", "allocations")))
	available := uint64(0)
	if usable > allocated {
		available = usable - allocated
	}
	detail := fmt.Sprintf("range=%s pool=kube-system/%s usable=%d allocated=%d available=%d required=%d", config.Range, poolName, usable, allocated, available, required)
	if available < uint64(required) {
		return NewResult(Fail, name+" has insufficient free IPs", detail, reason)
	}
	return NewResult(Pass, name+" has enough free IPs", detail, reason)
}

func harvesterSetting(ctx context.Context, env *Environment, name string, optional bool) (string, error) {
	setting, err := env.Get(ctx, "harvesterhci.io", "settings", "", name)
	if err != nil {
		if optional && apierrors.IsNotFound(unwrapAPIError(err)) {
			return "", nil
		}
		return "", err
	}
	return settingEffectiveValue(setting), nil
}

// Environment.Get adds context to API errors; inspect the wrapped chain.
func unwrapAPIError(err error) error {
	for {
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			return err
		}
		err = unwrapped
	}
}

func shareStorageNetwork(ctx context.Context, env *Environment, rwxValue string) (bool, error) {
	if rwxValue != "" {
		var value map[string]any
		if err := json.Unmarshal([]byte(rwxValue), &value); err != nil {
			return false, fmt.Errorf("rwx-network contains invalid JSON: %w", err)
		}
		shared, _ := value["share-storage-network"].(bool)
		return shared, nil
	}
	setting, err := env.Get(ctx, "longhorn.io", "settings", "longhorn-system", "storage-network-for-rwx-volume-enabled")
	if err != nil {
		if apierrors.IsNotFound(unwrapAPIError(err)) {
			return false, nil
		}
		return false, err
	}
	return nestedString(setting.Object, "value") == "true", nil
}

func checkStorageNetworkIPs(ctx context.Context, env *Environment) Result {
	rwxValue, err := harvesterSetting(ctx, env, "rwx-network", true)
	if err != nil {
		return NewResult(Error, "could not inspect rwx-network", err.Error())
	}
	storageValue, err := harvesterSetting(ctx, env, "storage-network", false)
	if err != nil {
		return NewResult(Error, "could not inspect storage-network", err.Error())
	}
	shared, err := shareStorageNetwork(ctx, env, rwxValue)
	if err != nil {
		return NewResult(Error, "could not determine RWX storage network mode", err.Error())
	}
	if shared && (storageValue == "" || storageValue == "null") {
		return NewResult(Fail, "RWX is configured to share an empty storage network")
	}
	if storageValue == "" || storageValue == "null" {
		return NewResult(Skipped, "storage network is not configured")
	}
	config, err := parseNetwork(storageValue)
	if err != nil {
		return NewResult(Error, "storage-network contains invalid JSON", err.Error())
	}
	instanceManagers, err := env.List(ctx, "longhorn.io", "instancemanagers", "longhorn-system", metav1.ListOptions{})
	if err != nil {
		return NewResult(Error, "could not count Longhorn instance managers", err.Error())
	}
	backingManagers, err := env.List(ctx, "longhorn.io", "backingimagemanagers", "longhorn-system", metav1.ListOptions{})
	if err != nil {
		return NewResult(Error, "could not count Longhorn backing image managers", err.Error())
	}
	required := int64(len(instanceManagers.Items) + len(backingManagers.Items))
	reason := fmt.Sprintf("required for %d new instance managers and %d new backing image managers", len(instanceManagers.Items), len(backingManagers.Items))
	if shared {
		required++
		reason += ", plus one upgrade repository RWX volume"
	}
	if required == 0 {
		return NewResult(Skipped, "no Longhorn manager replacement IPs are required")
	}
	return availableNetworkIPs(ctx, env, "storage network", config, required, reason)
}

func checkRWXNetworkIPs(ctx context.Context, env *Environment) Result {
	rwxValue, err := harvesterSetting(ctx, env, "rwx-network", true)
	if err != nil {
		return NewResult(Error, "could not inspect rwx-network", err.Error())
	}
	if rwxValue == "" || rwxValue == "null" {
		return NewResult(Skipped, "dedicated RWX network is not configured")
	}
	shared, err := shareStorageNetwork(ctx, env, rwxValue)
	if err != nil {
		return NewResult(Error, "could not determine RWX storage network mode", err.Error())
	}
	if shared {
		return NewResult(Skipped, "RWX shares the storage network")
	}
	var setting struct {
		Network json.RawMessage `json:"network"`
	}
	if err := json.Unmarshal([]byte(rwxValue), &setting); err != nil {
		return NewResult(Error, "rwx-network contains invalid JSON", err.Error())
	}
	if len(setting.Network) == 0 || string(setting.Network) == "null" {
		return NewResult(Skipped, "dedicated RWX network is not configured")
	}
	config, err := parseNetwork(string(setting.Network))
	if err != nil {
		return NewResult(Error, "dedicated RWX network configuration is invalid", err.Error())
	}
	return availableNetworkIPs(ctx, env, "dedicated RWX network", config, 1, "required for the upgrade repository RWX volume")
}
