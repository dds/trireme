package iptablesctrl

import (
	"fmt"
	"os/exec"

	"github.com/bvandewalle/go-ipset/ipset"
	"go.uber.org/zap"
)

// updateTargetNetworks updates the set of target networks. Tries to minimize
// read/writes to the ipset structures
func (i *Instance) updateTargetNetworks(old, new []string) error {

	deleteMap := map[string]bool{}
	for _, net := range old {
		deleteMap[net] = true
	}

	for _, net := range new {
		if _, ok := deleteMap[net]; ok {
			deleteMap[net] = false
			continue
		}

		if err := i.targetSet.Add(net, 0); err != nil {
			return fmt.Errorf("Failed to update target set")
		}
	}

	for net, delete := range deleteMap {
		if delete {
			if err := i.targetSet.Del(net); err != nil {
				zap.L().Debug("Failed to remove network from set")
			}
		}
	}
	return nil
}

// createTargetSet creates a new target set
func (i *Instance) createTargetSet(networks []string) error {

	ips, err := i.ipset.NewIpset(targetNetworkSet, "hash:net", &ipset.Params{})
	if err != nil {
		return fmt.Errorf("Couldn't create IPSet for %s: %s", targetNetworkSet, err)
	}

	i.targetSet = ips

	for _, net := range networks {
		if err := i.targetSet.Add(net, 0); err != nil {
			return fmt.Errorf("Error adding ip %s to target networks IPSet: %s", net, err)
		}
	}

	return nil
}

//Not using ipset from coreos library they don't support bitmap:port
func (i *Instance) createPUPortSet(setname string) error {
	//Bitmap type is not supported by the ipset library
	//_, err := i.ipset.NewIpset(setname, "hash:port", &ipset.Params{})
	path, _ := exec.LookPath("ipset")
	out, err := exec.Command(path, "create", setname, "bitmap:port", "range", "0-65535", "timeout", "0").CombinedOutput()
	if err != nil {
		zap.L().Error("Error Creating Set", zap.String("Ipset Output", string(out)))
	}
	return err

}
