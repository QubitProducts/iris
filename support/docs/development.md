Development
============

Dependency management is handled by [Dep](https://golang.github.io/dep). Run `dep ensure` to download all dependencies.

If the configuaration protobuf has changed, [Prototool](https://github.com/uber/p^rototool) is required to rebuild it.
Once Prototool is installed, run `make gen-protos` to regenerate the protobuf code.


Testing On Kubernetes
-------------------

[Skaffold](https://github.com/GoogleContainerTools/skaffold) and [Helm](https://www.helm.sh) are required for testing and deploying Iris to a Kubernetes cluster. 

Edit `skaffold.yaml` to set the Iris configuration as appropriate and run `skaffold dev` launch a development pipeline


Testing Locally
---------------

Install [certstrap](https://github.com/square/certstrap)

Generate certificates:

```
CS="certstrap --depot-path support/local-conf/certs/local"

# Generate CA certificate
$CS init --common-name "IrisCA"

# Generate certificate signing request
$CS request-cert --common-name "IrisServer"

# Sign the CSR
$CS sign IrisServer --CA "IrisCA"
```

Edit `support/local-conf/iris.yaml` to match your configuration 

Edit `support/local-conf/envoy.yaml` to point the `iris_cluster` to the address Iris will be running on

Run `make launch_iris` to launch an Iris instance

Run `make launch_envoy` to launch an Envoy instance

