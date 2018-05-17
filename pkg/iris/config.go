package iris

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"

	"github.com/ghodss/yaml"
	"github.com/gogo/protobuf/jsonpb"
	"github.com/QubitProducts/iris/pkg/v1pb"
)

type ValidationErr struct {
	Context interface{}
	Err     error
}

func (v ValidationErr) Error() string {
	return fmt.Sprintf("%+v [%+v]", v.Err, v.Context)
}

func LoadConfig(r io.Reader) (*v1pb.Config, error) {
	yamlBytes, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}

	jsonBytes, err := yaml.YAMLToJSON(yamlBytes)
	if err != nil {
		return nil, err
	}

	var conf v1pb.Config
	if err = jsonpb.Unmarshal(bytes.NewReader(jsonBytes), &conf); err != nil {
		return nil, err
	}

	return &conf, nil
}

func ValidateConfig(conf *v1pb.Config) []ValidationErr {
	var errors []ValidationErr

	for _, listener := range conf.Listeners {
		if err := listener.Validate(); err != nil {
			errors = append(errors, ValidationErr{Context: listener, Err: err})
		}
	}

	for _, route := range conf.Routes {
		if err := route.Validate(); err != nil {
			errors = append(errors, ValidationErr{Context: route, Err: err})
		}
	}

	for _, cluster := range conf.Clusters {
		if cluster.ServiceName == "" {
			errors = append(errors, ValidationErr{Context: cluster, Err: fmt.Errorf("Service name must be defined")})
		}

		if cluster.PortName == "" {
			errors = append(errors, ValidationErr{Context: cluster, Err: fmt.Errorf("Port name must be defined")})
		}

		if err := cluster.Config.Validate(); err != nil {
			errors = append(errors, ValidationErr{Context: cluster, Err: err})
		}
	}

	return errors
}
