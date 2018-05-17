package iris

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadConfig(t *testing.T) {
	f, err := os.Open("testdata/conf1.yaml")
	assert.NoError(t, err)
	defer f.Close()

	conf, err := LoadConfig(f)
	assert.NoError(t, err)

	assert.Equal(t, "test", conf.Iris.Namespace)
}

func TestValidateConfig(t *testing.T) {
	f, err := os.Open("testdata/conf2.yaml")
	assert.NoError(t, err)
	defer f.Close()

	conf, err := LoadConfig(f)
	assert.NoError(t, err)

	r := ValidateConfig(conf)
	assert.NotNil(t, r)
	assert.Len(t, r, 1)
	assert.EqualError(t, r[0].Err, "invalid Cluster.Name: value length must be at least 1 bytes")
}
