package acceptance

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	ggcrremote "github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/buildpacks/imgutil"
	"github.com/buildpacks/imgutil/local"
	"github.com/buildpacks/imgutil/remote"
	h "github.com/buildpacks/imgutil/testhelpers"
)

var registryHost, registryPort string

func newTestImageName() string {
	return registryHost + ":" + registryPort + "/imgutil-acceptance-" + h.RandString(10)
}

func TestReproducibility(t *testing.T) {
	dockerConfigDir, err := os.MkdirTemp("", "test.docker.config.dir")
	h.AssertNil(t, err)
	dockerRegistry := h.NewDockerRegistry(h.WithAuth(dockerConfigDir))
	os.Setenv("DOCKER_CONFIG", dockerRegistry.DockerDirectory)
	dockerRegistry.Start(t)

	defer func() {
		os.RemoveAll(dockerConfigDir)
		dockerRegistry.Stop(t)
		os.Unsetenv("DOCKER_CONFIG")
	}()

	registryHost = dockerRegistry.Host
	registryPort = dockerRegistry.Port

	dockerClient := h.DockerCli(t)

	daemonInfo, err := dockerClient.Info(context.TODO())
	h.AssertNil(t, err)

	daemonOS := daemonInfo.OSType

	runnableBaseImageName := h.RunnableBaseImage(daemonOS)
	h.PullIfMissing(t, dockerClient, runnableBaseImageName)

	testCases := map[string]struct {
	}{
		"remote/remote": {},
		"local/local":   {},
		"remote/local":  {},
	}

	for name := range testCases {
		t.Run(name, func(t *testing.T) {
			imageName1 := newTestImageName()
			imageName2 := newTestImageName()
			labelKey := "label-key-" + h.RandString(10)
			labelVal := "label-val-" + h.RandString(10)
			envKey := "env-key-" + h.RandString(10)
			envVal := "env-val-" + h.RandString(10)
			workingDir := "working-dir-" + h.RandString(10)

			layer1, err := h.CreateSingleFileLayerTar(fmt.Sprintf("/new-layer-%s.txt", h.RandString(10)), "new-layer-"+h.RandString(10), daemonOS)
			h.AssertNil(t, err)

			layer2, err := h.CreateSingleFileLayerTar(fmt.Sprintf("/new-layer-%s.txt", h.RandString(10)), "new-layer-"+h.RandString(10), daemonOS)
			h.AssertNil(t, err)

			mutateAndSave := func(t *testing.T, img imgutil.Image) {
				h.AssertNil(t, img.AddLayer(layer1))
				h.AssertNil(t, img.AddLayer(layer2))
				h.AssertNil(t, img.SetLabel(labelKey, labelVal))
				h.AssertNil(t, img.SetEnv(envKey, envVal))
				h.AssertNil(t, img.SetEntrypoint("some", "entrypoint"))
				h.AssertNil(t, img.SetCmd("some", "cmd"))
				h.AssertNil(t, img.SetWorkingDir(workingDir))
				h.AssertNil(t, img.Save())
			}

			defer func() {
				// clean up any local images
				h.DockerRmi(dockerClient, imageName1)
				h.DockerRmi(dockerClient, imageName2)
				h.AssertNil(t, os.Remove(layer1))
				h.AssertNil(t, os.Remove(layer2))
			}()

			// split the test name at the /
			imageTypes := strings.Split(name, "/")
			image1Type := imageTypes[0]
			image2Type := imageTypes[1]

			switch image1Type {
			case "local":
				img, err := local.NewImage(imageName1, dockerClient, local.FromBaseImage(runnableBaseImageName))
				h.AssertNil(t, err)
				mutateAndSave(t, img)
				h.PushImage(t, dockerClient, imageName1)
			case "remote":
				img, err := remote.NewImage(imageName1, authn.DefaultKeychain, remote.FromBaseImage(runnableBaseImageName))
				h.AssertNil(t, err)
				mutateAndSave(t, img)
			default:
				t.Fatalf("unsupported image type: %s", image1Type)
			}

			switch image2Type {
			case "local":
				img, err := local.NewImage(imageName2, dockerClient, local.FromBaseImage(runnableBaseImageName))
				h.AssertNil(t, err)
				mutateAndSave(t, img)
				h.PushImage(t, dockerClient, imageName2)
			case "remote":
				img, err := remote.NewImage(imageName2, authn.DefaultKeychain, remote.FromBaseImage(runnableBaseImageName))
				h.AssertNil(t, err)
				mutateAndSave(t, img)
			default:
				t.Fatalf("unsupported image type: %s", image2Type)
			}

			compare(t, imageName1, imageName2)
		})
	}
}

func compare(t *testing.T, img1, img2 string) {
	t.Helper()

	ref1, err := name.ParseReference(img1, name.WeakValidation)
	h.AssertNil(t, err)

	ref2, err := name.ParseReference(img2, name.WeakValidation)
	h.AssertNil(t, err)

	auth1, err := authn.DefaultKeychain.Resolve(ref1.Context().Registry)
	h.AssertNil(t, err)

	auth2, err := authn.DefaultKeychain.Resolve(ref2.Context().Registry)
	h.AssertNil(t, err)

	v1img1, err := ggcrremote.Image(ref1, ggcrremote.WithAuth(auth1))
	h.AssertNil(t, err)

	v1img2, err := ggcrremote.Image(ref2, ggcrremote.WithAuth(auth2))
	h.AssertNil(t, err)

	cfg1, err := v1img1.ConfigFile()
	h.AssertNil(t, err)

	cfg2, err := v1img2.ConfigFile()
	h.AssertNil(t, err)

	// images that were created locally may have `DockerVersion` equal to "dev" and missing `Config.Image` if the daemon uses containerd storage
	cfg1.DockerVersion = ""
	cfg2.DockerVersion = ""
	cfg1.Config.Image = ""
	cfg2.Config.Image = ""

	h.AssertEq(t, cfg1, cfg2)

	h.AssertEq(t, ref1.Identifier(), ref2.Identifier())
}
