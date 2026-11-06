# Networking

How an environment is reachable from outside the cluster is decided by its
`DevEnvironmentClass` (`spec.network.mode`). The web terminal, the VNC desktop
and the SSH gateway work in every mode.

| Mode | Address | Ports | Requirements |
|---|---|---|---|
| `gateway` (default) | none, `ssh -p 2222 <env>@<gateway>` | SSH, `-L`/`-R` forwarding | none |
| `loadBalancer` | one LoadBalancer IP per environment | 22 + ports listed in the environment | a LoadBalancer implementation |
| `multus` | one secondary interface (e.g. a LAN IP) | all ports | Multus + a NetworkAttachmentDefinition |

## loadBalancer

```yaml
apiVersion: devenv.tinyrack.net/v1alpha1
kind: DevEnvironmentClass
metadata:
  name: lb
spec:
  network:
    mode: loadBalancer
    loadBalancer:
      # optional
      loadBalancerClassName: io.cilium/l2-announcer
      labels: { pool: lan }
      annotations: {}
```

Each environment gets a `<name>-lb` Service with SSH plus the ports added in
the console (Exposed ports). The Service survives stop/start, so the address
is kept until the environment is deleted.

## multus

The class references a NetworkAttachmentDefinition (NAD) you create. devenv
does not install CNI plugins.

```yaml
apiVersion: devenv.tinyrack.net/v1alpha1
kind: DevEnvironmentClass
metadata:
  name: lan
spec:
  network:
    mode: multus
    multus:
      networkAttachment: devenv/lan-dhcp   # namespace/name or name
      interface: net1                      # default
      stableMAC: true                      # derive the MAC from the environment UID
```

The IP reported by Multus (`k8s.v1.cni.cncf.io/network-status`) is shown in
the console and in `status.ip`.

### LAN IP from the network's DHCP server (macvlan + dhcp)

Requirements on every node:

- the `macvlan` and `dhcp` CNI plugins in the CNI bin directory
- the DHCP daemon running: `dhcp daemon` (e.g. a hostNetwork DaemonSet
  mounting `/run/cni` and the host's `/proc`... see the plugin docs)

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: lan-dhcp
  namespace: devenv
spec:
  config: |-
    {
      "cniVersion": "1.0.0",
      "type": "macvlan",
      "master": "eth0",
      "mode": "bridge",
      "capabilities": { "mac": true },
      "ipam": {
        "type": "dhcp",
        "provide": [
          { "option": "dhcp-client-identifier", "fromArg": "K8S_POD_NAME" },
          { "option": "host-name", "fromArg": "K8S_POD_NAME" }
        ]
      }
    }
```

To keep the same IP across restarts:

- `stableMAC: true` keeps the MAC address, and
- `dhcp-client-identifier` from `K8S_POD_NAME` keeps the DHCP client ID
  (the plugin's default ID contains the container ID, which changes on every
  restart). Environment pods are named after the environment.

`host-name` registers the environment name with DHCP servers that update DNS.

Notes:

- macvlan interfaces can not talk to their parent host (the node itself).
- Traffic on the secondary interface bypasses Kubernetes NetworkPolicies;
  the environment is directly on that network. Only key based SSH is enabled
  in the official images.
- Reserve the IP of important environments on the DHCP server using the MAC
  shown in the console.
