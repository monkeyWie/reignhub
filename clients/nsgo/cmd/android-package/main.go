// Repackage NSGo's standalone APK with an official embedding host. The Go
// executable/assets, app ID and existing project signing key are preserved.
package main

import (
	"archive/zip"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"github.com/nsgo-dev/nsgo/cmd/nsgo/apk"
	"go.yaml.in/yaml/v3"
	"io"
	"os"
	"strings"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func read(path string) []byte { b, e := os.ReadFile(path); must(e); return b }
func main() {
	input := flag.String("input", "", "NSGo APK")
	output := flag.String("output", "", "APK output")
	dex := flag.String("dex", "", "host classes.dex")
	manifest := flag.String("manifest", "native/android/AndroidManifest.xml", "launcher XML")
	config := flag.String("config", "nsgo.yaml", "NSGo config")
	keyFile := flag.String("key", ".nsgo/android/debug.pem", "existing APK signing key")
	version := flag.String("version", "", "override application version")
	buildNumber := flag.Int("build-number", 0, "override application build number")
	sdkManifest := flag.String("sdk-manifest", "", "prepare XML for the Android SDK manifest compiler")
	flag.Parse()
	var cfg struct {
		Name, ID, Version string
		Sign              struct{ Keystore string }
		BuildNumber       int `yaml:"build_number"`
		Android           struct {
			Permissions []string
			TargetSDK   int `yaml:"target_sdk"`
		}
	}
	must(yaml.Unmarshal(read(*config), &cfg))
	if *version != "" {
		cfg.Version = *version
	}
	if *buildNumber > 0 {
		cfg.BuildNumber = *buildNumber
	}
	if cfg.BuildNumber == 0 {
		cfg.BuildNumber = apk.VersionCode(cfg.Version)
	}
	if *sdkManifest != "" {
		prepared, err := prepareSDKManifest(read(*manifest), apk.Manifest{Package: cfg.ID, Label: cfg.Name, VersionName: cfg.Version, VersionCode: cfg.BuildNumber, TargetSDK: cfg.Android.TargetSDK, Permissions: cfg.Android.Permissions})
		must(err)
		must(os.WriteFile(*sdkManifest, prepared, 0600))
		return
	}
	if cfg.Sign.Keystore != "" {
		panic("native Android host packaging requires production keystore support before release signing; refusing to replace a release signature with the debug key")
	}
	key := &apk.Key{}
	data := read(*keyFile)
	for len(data) > 0 {
		block, rest := pem.Decode(data)
		data = rest
		if block == nil {
			break
		}
		switch block.Type {
		case "RSA PRIVATE KEY":
			var e error
			key.Private, e = x509.ParsePKCS1PrivateKey(block.Bytes)
			must(e)
		case "PRIVATE KEY":
			p, e := x509.ParsePKCS8PrivateKey(block.Bytes)
			must(e)
			key.Private = p.(*rsa.PrivateKey)
		case "CERTIFICATE":
			var e error
			key.Cert, e = x509.ParseCertificate(block.Bytes)
			must(e)
		}
	}
	if key.Private == nil || key.Cert == nil {
		panic("existing NSGo signing key is required")
	}
	z, e := zip.OpenReader(*input)
	must(e)
	defer z.Close()
	files := []apk.File{{Name: "classes.dex", Data: read(*dex)}}
	var icon []byte
	for _, file := range z.File {
		if file.Name == "AndroidManifest.xml" || file.Name == "resources.arsc" || strings.HasPrefix(file.Name, "classes") || strings.HasPrefix(file.Name, "META-INF/") {
			continue
		}
		r, e := file.Open()
		must(e)
		b, e := io.ReadAll(r)
		must(e)
		r.Close()
		if file.Name == "res/mipmap-xxxhdpi-v4/ic_launcher.png" {
			icon = b
			continue
		}
		files = append(files, apk.File{Name: file.Name, Data: b, Stored: file.Method == zip.Store})
	}
	if cfg.BuildNumber == 0 {
		cfg.BuildNumber = apk.VersionCode(cfg.Version)
	}
	out, e := os.Create(*output)
	must(e)
	must(apk.Write(out, read(*manifest), apk.Manifest{Package: cfg.ID, Label: cfg.Name, VersionName: cfg.Version, VersionCode: cfg.BuildNumber, Permissions: cfg.Android.Permissions, TargetSDK: cfg.Android.TargetSDK, Icon: icon}, files, key))
	must(out.Close())
	fmt.Println("Packaged Android host:", *output)
}
