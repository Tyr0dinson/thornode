package config

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/viper"
	. "gopkg.in/check.v1"
	"gopkg.in/yaml.v2"
)

func TestPackage(t *testing.T) { TestingT(t) }

type Test struct{}

var _ = Suite(&Test{})

func (Test) TestAllChainsReturned(c *C) {
	b := Bifrost{}
	c.Assert(len(b.GetChains()), Equals, reflect.TypeOf(b.Chains).NumField())
}

func (Test) TestAllDefaultDefined(c *C) {
	// In order to override configuration values, defaults must first be defined
	// in the default YAML file. This test ensures all fields have defaults defined.

	confMap := map[interface{}]interface{}{}
	err := yaml.Unmarshal(defaultConfig, &confMap)
	c.Assert(err, IsNil)

	// recursive check defaults for all fields in config struct
	check(c, []string{}, confMap, reflect.TypeOf(Config{}))
}

func (Test) TestXMRSignerEndpointDefault(c *C) {
	confMap := map[interface{}]interface{}{}
	err := yaml.Unmarshal(defaultConfig, &confMap)
	c.Assert(err, IsNil)

	bifrostRaw, ok := confMap["bifrost"]
	c.Assert(ok, Equals, true)
	bifrostMap, ok := bifrostRaw.(map[interface{}]interface{})
	c.Assert(ok, Equals, true)

	chainsRaw, ok := bifrostMap["chains"]
	c.Assert(ok, Equals, true)
	chainsMap, ok := chainsRaw.(map[interface{}]interface{})
	c.Assert(ok, Equals, true)

	xmrRaw, ok := chainsMap["xmr"]
	c.Assert(ok, Equals, true)
	xmrMap, ok := xmrRaw.(map[interface{}]interface{})
	c.Assert(ok, Equals, true)

	signerEndpoint, ok := xmrMap["signer_endpoint"]
	c.Assert(ok, Equals, true)
	c.Assert(signerEndpoint, Equals, "http://127.0.0.1:8283")
}

func (Test) TestXMRDaemonURLOverridesBifrostRPCHost(c *C) {
	prevXMRDaemonURL, hadXMRDaemonURL := os.LookupEnv("XMR_DAEMON_URL")
	prevXMRHost, hadXMRHost := os.LookupEnv("XMR_HOST")
	defer func() {
		if hadXMRDaemonURL {
			c.Assert(os.Setenv("XMR_DAEMON_URL", prevXMRDaemonURL), IsNil)
		} else {
			c.Assert(os.Unsetenv("XMR_DAEMON_URL"), IsNil)
		}
		if hadXMRHost {
			c.Assert(os.Setenv("XMR_HOST", prevXMRHost), IsNil)
		} else {
			c.Assert(os.Unsetenv("XMR_HOST"), IsNil)
		}
		viper.Reset()
	}()

	viper.Reset()
	c.Assert(os.Setenv("XMR_DAEMON_URL", "http://daemon.example:18081"), IsNil)
	c.Assert(os.Unsetenv("XMR_HOST"), IsNil)

	Init()

	c.Assert(GetBifrost().Chains.XMR.RPCHost, Equals, "http://daemon.example:18081")
}

func check(c *C, path []string, cm map[interface{}]interface{}, t reflect.Type) {
	// ensure all config fields are defined
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("mapstructure")
		tagPath := strings.Join(append(path, tag), ".")

		// skip some fields, since there are environment variables we won't override
		if strings.HasPrefix(tagPath, "bifrost.signer.block_scanner") {
			continue
		}
		if strings.HasPrefix(tagPath, "bifrost.thorchain.back_off") {
			continue
		}
		if t.Field(i).Name == "SignerPasswd" {
			continue
		}

		// assert the field is defined in config
		if _, ok := cm[tag]; !ok {
			c.Fatalf("missing default for %s %s", tagPath, t.Field(i).Type)
		}

		// trunk-ignore-all(golangci-lint/forcetypeassert)
		switch t.Field(i).Type.Kind() {

		case reflect.Struct: // recurse if this is a nested struct
			check(c, append(path, tag), cm[tag].(map[interface{}]interface{}), t.Field(i).Type)

		case reflect.Map: // recurse on each value if this is a map
			for k, v := range cm[tag].(map[interface{}]interface{}) {
				check(c, append(path, tag, k.(string)), v.(map[interface{}]interface{}), t.Field(i).Type.Elem())
			}
		}
	}

	// ensure all defaults are defined in config
	for k := range cm {
		found := false
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).Tag.Get("mapstructure") == k {
				found = true
				break
			}
		}
		if !found {
			c.Fatalf("unknown mapping: %s", strings.Join(append(path, k.(string)), "."))
		}
	}
}
