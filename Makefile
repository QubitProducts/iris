# Update prototool.yaml if this path is changed
DATA_PLANE_API_LOC:=/tmp/iris/data-plane-api
DATA_PLANE_API_REPO:=https://github.com/envoyproxy/data-plane-api.git
PROTO_DIR:=pkg/v1pb
DOCKER_IMAGE:=qubit/iris
LOCAL_CONF_DIR:=$(shell pwd)/support/local-conf

.PHONY: gen-protos test container docker publish launch_iris launch_envoy clean


$(DATA_PLANE_API_LOC):
	git clone $(DATA_PLANE_API_REPO) $(DATA_PLANE_API_LOC)

gen-protos: $(DATA_PLANE_API_LOC)
	@prototool all $(PROTO_DIR)

test:
	go test ./...

container: 
	CGO_ENABLED=0 GOOS=linux go build -a -ldflags '-w -s' .

docker:
	@docker build --rm -t $(DOCKER_IMAGE) .

publish: docker
	@gcloud docker -- push $(DOCKER_IMAGE) 

launch_iris:
	@go run main.go serve \
		--conf=$(LOCAL_CONF_DIR)/iris.yaml \
		--tls_ca=$(LOCAL_CONF_DIR)/certs/local/IrisCA.crt \
		--tls_crt=$(LOCAL_CONF_DIR)/certs/local/IrisServer.crt \
		--tls_key=$(LOCAL_CONF_DIR)/certs/local/IrisServer.key \
		--watch_conf=true \
		--debug=true

launch_envoy:
	@docker run \
		-it \
		-v $(LOCAL_CONF_DIR):/conf \
		-p 9000:9000 \
		-p 8082:8082 \
		envoyproxy/envoy-alpine-debug /usr/local/bin/envoy --config-path /conf/envoy.yaml --v2-config-only -l debug

clean:
	@-go clean
	@-rm -rf $(DATA_PLANE_API_LOC)
