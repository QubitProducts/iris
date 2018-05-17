FROM golang:1.10-alpine as builder
ARG SRC_DIR=/go/src/github.com/QubitProducts/iris
RUN apk --no-cache add --update make
ADD . $SRC_DIR
WORKDIR $SRC_DIR
RUN make container 

FROM scratch
COPY --from=builder /go/src/github.com/QubitProducts/iris/iris /iris
ENV GRPC_GO_LOG_SEVERITY_LEVEL WARNING
ENTRYPOINT ["/iris"]


