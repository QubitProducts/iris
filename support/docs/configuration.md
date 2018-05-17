Configuration
=============

Envoy Metadata From Pod Annotations
-----------------------------------

Custom endpoint metadata useful for defining routing rules are derived from pod annotations prefixed with `iris.qubit.com`.

Set the `iris.qubit.com/canary` annotation to `true` or `false` if the pod is a canary. This is a special annotation that
will also automatically become available under `envoy.lb` metadata to indicate to Envoy that this pod is a canary pod.

Any other annotations prefixed with `iris.qubit.com/` will be available as Envoy metadata under the `com.qubit.iris` key.


**Example:**

Assume that the following annotations are defined in the pod spec:

```yaml
...
metadata:
    annotations:
        prometheus.io/scrape: "true"
        iris.qubit.com/canary: "true"
        iris.qubit.com/foo: "bar"
...
```

A routing rule can be specified as follows to send traffic to canary instances where `foo` is set to `bar`

```yaml
...
- name: my_route
  virtual_hosts:
    - name: my_svc
      domains: ["mysvc.mydomain.com"]
      routes:
        - match:
            prefix: "/v2alpha" 
          route:
            cluster: my_beta_cluster 
            timeout:  2s
            metadata_match: 
                filter_metadata:
                    "envoy.lb": {"canary": true}
                    "com.qubit.iris": {"foo": "bar"}
...
```

Iris Configuration File
-----------------------

Iris configuration can be defined in JSON or YAML formats. The general structure of the file is as follows:

```yaml
# Iris spcific configuration values
iris:
  # Kubernetes namespace to watch
  namespace: myapp
  # Log level (valid values are DEBUG, INFO, WARN and ERROR)
  log_level: DEBUG
  # Number of goroutines to use to watch and process pod events
  parallelism: 4
  # Default port to use if the iris.qubit.com/port annotation is absent from a pod
  default_pod_port: 8081
  # How long to buffer the events before publishing the state 
  state_refresh_period: 1s
  # Number of unpublished state changes allowed
  state_buffer_size: 16

# List of Envoy listeners
listeners:
  # Refer to https://www.envoyproxy.io/docs/envoy/latest/api-v2/api/v2/lds.proto 
  - name: my_proxy
    address:
      socket_address: { address: 0.0.0.0, port_value: 8082 }
    filter_chains:
      - filters:
        - name: envoy.http_connection_manager
          config:
            codec_type: AUTO
            stat_prefix: my_proxy
            generate_request_id: true
            rds:
              route_config_name: my_route
              config_source:
                ads: {}
            tracing:
              operation_name: egress
            http_filters:
              - name: envoy.router

# List of Envoy routes
routes:
  # Refer to https://www.envoyproxy.io/docs/envoy/latest/api-v2/api/v2/rds.proto
  - name: my_route
    virtual_hosts:
      - name: mysvc_svc
        domains: ["*"]
        routes:
          - match:
              prefix: "/company.app.v1.MyService/" 
            route:
              cluster: grpc_read_cluster 
              timeout:  1s
              retry_policy: { "num_retries": 3, "retry_on": "5xx;resource-exhausted", "per_try_timeout": "0.3s" }

# List of Envoy clusters
clusters:
    # Name of the Kubernetes service that provides the endpoints for this Envoy cluster 
  - service_name: "read-svc"
    # Name of the port to forward traffic to
    port_name: grpc
    # Config embeds an Envoy cluster definition. Refer to https://www.envoyproxy.io/docs/envoy/latest/api-v2/api/v2/cds.proto#cluster
    config:
      name: grpc_read_cluster
      connect_timeout: 5s
      type: EDS
      eds_cluster_config:
        eds_config:
          ads: {}
      lb_policy: RANDOM
      http2_protocol_options: { "max_concurrent_streams": 256 }                        
      outlier_detection: {}
      circuit_breakers: 
        thresholds:
            - priority: DEFAULT
              max_connections: 128
              max_pending_requests: 128
              max_requests: 256
      health_checks:
        - timeout: 1s 
          interval: 15s
          interval_jitter: 2s
          healthy_threshold: 2
          unhealthy_threshold: 2
          grpc_health_check: {}
                tls_context: 
                  sni: "myapp.mydomain.com"
                  common_tls_context:
                    alpn_protocols: ["h2"]
                    tls_certificates:
                      - private_key:
                          filename: "/secrets/client.key"
                        certificate_chain:
                          filename: "/secrets/client.crt"
```

Envoy Configuration File 
------------------------

Assuming Iris is deployed as Kubernetes service in the `default` namespace, the following is a minimal 
Envoy configuration file to use Iris as the ADS.

```yaml
node:
  id: envoy
  cluster: envoy_cluster

admin:
  access_log_path: /tmp/admin_access.log
  address:
    socket_address: { address: 0.0.0.0, port_value: 9000 }

dynamic_resources:
  lds_config:
    ads: {}

  cds_config:
    ads: {}

  ads_config:
    api_type: GRPC
    refresh_delay: 5s
    cluster_names: [iris_cluster]
    grpc_services:
      timeout: 0.5s
      envoy_grpc:
        cluster_name: iris_cluster

static_resources:
  clusters:
    - name: envoy_cluster
      connect_timeout: 1s
      type: STATIC
      hosts:
        - { socket_address: {address: 127.0.0.1, port_value: 8082 } }

    - name: iris_cluster
      connect_timeout: 1s
      type: STRICT_DNS
      lb_policy: LEAST_REQUEST
      http2_protocol_options: {}
      hosts:
        - { socket_address: { address: iris.default.svc.cluster.local, port_value: 8080 } }
      tls_context:
        sni: "iris.default.svc.cluster.local"
        common_tls_context:
          alpn_protocols: "h2,http/1.1"
      health_checks:
        timeout: 0.5s
        interval: 10s
        unhealthy_threshold: 2
        healthy_threshold: 2
        grpc_health_check: {}

cluster_manager:
  local_cluster_name: envoy_cluster
```
