package local_test

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/sclevine/spec"
	"github.com/sclevine/spec/report"

	"github.com/buildpacks/imgutil"
	"github.com/buildpacks/imgutil/local"
	h "github.com/buildpacks/imgutil/testhelpers"
)

var localTestRegistry *h.DockerRegistry

func TestLocal(t *testing.T) {
	rand.Seed(time.Now().UTC().UnixNano())

	localTestRegistry = h.NewDockerRegistry()
	localTestRegistry.Start(t)
	defer localTestRegistry.Stop(t)

	spec.Run(t, "Image", testImage, spec.Sequential(), spec.Report(report.Terminal{}))
}

func newTestImageName() string {
	return "localhost:" + localTestRegistry.Port + "/pack-image-test-" + h.RandString(10)
}

func testImage(t *testing.T, when spec.G, it spec.S) {
	var (
		dockerClient          client.CommonAPIClient
		daemonOS              string
		runnableBaseImageName string
	)

	it.Before(func() {
		var err error
		dockerClient = h.DockerCli(t)

		daemonInfo, err := dockerClient.Info(context.TODO())
		h.AssertNil(t, err)

		daemonOS = daemonInfo.OSType
		runnableBaseImageName = h.RunnableBaseImage(daemonOS)

		h.AssertNil(t, h.PullImage(dockerClient, runnableBaseImageName))
	})

	when("#NewImage", func() {
		when("no base image is given", func() {
			it("returns an empty image", func() {
				_, err := local.NewImage(newTestImageName(), dockerClient)
				h.AssertNil(t, err)
			})

			it("sets sensible defaults from daemon for all required fields", func() {
				// os, architecture, and rootfs are required per https://github.com/opencontainers/image-spec/blob/master/config.md
				img, err := local.NewImage(newTestImageName(), dockerClient)
				h.AssertNil(t, err)
				h.AssertNil(t, img.Save())

				defer h.DockerRmi(dockerClient, img.Name())
				inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), img.Name())
				h.AssertNil(t, err)

				daemonInfo, err := dockerClient.Info(context.TODO())
				h.AssertNil(t, err)

				h.AssertEq(t, inspect.Os, daemonInfo.OSType)
				h.AssertEq(t, inspect.OsVersion, daemonInfo.OSVersion)
				h.AssertEq(t, inspect.Architecture, "amd64")
				h.AssertEq(t, inspect.RootFS.Type, "layers")
			})
		})

		when("#FromBaseImage", func() {
			when("base image exists", func() {
				var baseImageName = newTestImageName()
				var repoName = newTestImageName()

				it.After(func() {
					h.AssertNil(t, h.DockerRmi(dockerClient, baseImageName))
				})

				it("returns the local image", func() {
					baseImage, err := local.NewImage(baseImageName, dockerClient)
					h.AssertNil(t, err)

					h.AssertNil(t, baseImage.SetEnv("MY_VAR", "my_val"))
					h.AssertNil(t, baseImage.SetLabel("some.label", "some.value"))
					h.AssertNil(t, baseImage.Save())

					localImage, err := local.NewImage(repoName, dockerClient, local.FromBaseImage(baseImageName))
					h.AssertNil(t, err)

					labelValue, err := localImage.Label("some.label")
					h.AssertNil(t, err)
					h.AssertEq(t, labelValue, "some.value")
				})
			})

			when("base image does not exist", func() {
				it("doesn't error", func() {
					_, err := local.NewImage(
						newTestImageName(),
						dockerClient,
						local.FromBaseImage("some-bad-repo-name"),
					)

					h.AssertNil(t, err)
				})
			})

			when("base image and daemon os/architecture match", func() {
				it("uses the base image architecture/OS", func() {
					img, err := local.NewImage(newTestImageName(), dockerClient, local.FromBaseImage(runnableBaseImageName))
					h.AssertNil(t, err)
					h.AssertNil(t, img.Save())
					defer h.DockerRmi(dockerClient, img.Name())

					imgOS, err := img.OS()
					h.AssertNil(t, err)
					h.AssertEq(t, imgOS, daemonOS)

					inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), img.Name())
					h.AssertNil(t, err)
					h.AssertEq(t, inspect.Os, daemonOS)
					h.AssertEq(t, inspect.Architecture, "amd64")
					h.AssertEq(t, inspect.RootFS.Type, "layers")

					h.AssertEq(t, img.Found(), true)
				})
			})

			when("base image and daemon architecture do not match", func() {
				it("uses the base image architecture", func() {
					armBaseImageName := "arm64v8/busybox@sha256:50edf1d080946c6a76989d1c3b0e753b62f7d9b5f5e66e88bef23ebbd1e9709c"
					expectedArmArch := "arm64"
					if daemonOS == "windows" {
						// this nanoserver windows/arm image exists and pulls. Not sure whether it works for anything else.
						armBaseImageName = "mcr.microsoft.com/windows/nanoserver@sha256:29e2270953589a12de7a77a7e77d39e3b3e9cdfd243c922b3b8a63e2d8a71026"
						expectedArmArch = "arm"
					}

					h.PullImage(dockerClient, armBaseImageName)

					img, err := local.NewImage(newTestImageName(), dockerClient, local.FromBaseImage(armBaseImageName))
					h.AssertNil(t, err)
					h.AssertNil(t, img.Save())
					defer h.DockerRmi(dockerClient, img.Name())

					imgArch, err := img.Architecture()
					h.AssertNil(t, err)

					h.AssertEq(t, imgArch, expectedArmArch)
				})
			})
		})

		when("#WithPreviousImage", func() {
			when("previous image does not exist", func() {
				it("doesn't error", func() {
					_, err := local.NewImage(
						newTestImageName(),
						dockerClient,
						local.WithPreviousImage("some-bad-repo-name"),
					)

					h.AssertNil(t, err)
				})
			})
		})
	})

	when("#Label", func() {
		when("image exists", func() {
			var repoName = newTestImageName()

			it.Before(func() {
				existingImage, err := local.NewImage(repoName, dockerClient)
				h.AssertNil(t, err)

				h.AssertNil(t, existingImage.SetLabel("mykey", "myvalue"))
				h.AssertNil(t, existingImage.SetLabel("other", "data"))
				h.AssertNil(t, existingImage.Save())
			})

			it.After(func() {
				h.AssertNil(t, h.DockerRmi(dockerClient, repoName))
			})

			it("returns the label value", func() {
				img, err := local.NewImage(repoName, dockerClient, local.FromBaseImage(repoName))
				h.AssertNil(t, err)

				label, err := img.Label("mykey")
				h.AssertNil(t, err)
				h.AssertEq(t, label, "myvalue")
			})

			it("returns an empty string for a missing label", func() {
				img, err := local.NewImage(repoName, dockerClient)
				h.AssertNil(t, err)

				label, err := img.Label("missing-label")
				h.AssertNil(t, err)
				h.AssertEq(t, label, "")
			})
		})

		when("image NOT exists", func() {
			it("returns an empty string", func() {
				img, err := local.NewImage(newTestImageName(), dockerClient)
				h.AssertNil(t, err)

				label, err := img.Label("some-label")
				h.AssertNil(t, err)
				h.AssertEq(t, label, "")
			})
		})
	})

	when("#Env", func() {
		when("image exists", func() {
			var repoName = newTestImageName()

			it.Before(func() {
				existingImage, err := local.NewImage(repoName, dockerClient)
				h.AssertNil(t, err)

				h.AssertNil(t, existingImage.SetEnv("MY_VAR", "my_val"))
				h.AssertNil(t, existingImage.Save())
			})

			it.After(func() {
				h.AssertNil(t, h.DockerRmi(dockerClient, repoName))
			})

			it("returns the label value", func() {
				img, err := local.NewImage(repoName, dockerClient, local.FromBaseImage(repoName))
				h.AssertNil(t, err)

				val, err := img.Env("MY_VAR")
				h.AssertNil(t, err)
				h.AssertEq(t, val, "my_val")
			})

			it("returns an empty string for a missing label", func() {
				img, err := local.NewImage(repoName, dockerClient, local.FromBaseImage(repoName))
				h.AssertNil(t, err)

				val, err := img.Env("MISSING_VAR")
				h.AssertNil(t, err)
				h.AssertEq(t, val, "")
			})
		})

		when("image NOT exists", func() {
			it("returns an empty string", func() {
				img, err := local.NewImage(newTestImageName(), dockerClient)
				h.AssertNil(t, err)

				val, err := img.Env("SOME_VAR")
				h.AssertNil(t, err)
				h.AssertEq(t, val, "")
			})
		})
	})

	when("#Name", func() {
		it("always returns the original name", func() {
			var repoName = newTestImageName()

			img, err := local.NewImage(repoName, dockerClient)
			h.AssertNil(t, err)

			h.AssertEq(t, img.Name(), repoName)
		})
	})

	when("#CreatedAt", func() {
		it("returns the containers created at time", func() {
			img, err := local.NewImage(newTestImageName(), dockerClient, local.FromBaseImage(runnableBaseImageName))
			h.AssertNil(t, err)

			// based on static base image refs
			expectedTime := time.Date(2018, 10, 2, 17, 19, 34, 239926273, time.UTC)
			if daemonOS == "windows" {
				expectedTime = time.Date(2020, 03, 04, 13, 28, 48, 673000000, time.UTC)
			}

			createdTime, err := img.CreatedAt()

			h.AssertNil(t, err)
			h.AssertEq(t, createdTime, expectedTime)
		})
	})

	when("#Identifier", func() {
		var repoName = newTestImageName()
		var baseImageName = newTestImageName()

		it.Before(func() {
			baseImage, err := local.NewImage(baseImageName, dockerClient)
			h.AssertNil(t, err)

			h.AssertNil(t, baseImage.SetLabel("existingLabel", "existingValue"))
			h.AssertNil(t, baseImage.Save())
		})

		it.After(func() {
			h.AssertNil(t, h.DockerRmi(dockerClient, baseImageName))
		})

		it("returns an Docker Image ID type identifier", func() {
			img, err := local.NewImage(repoName, dockerClient, local.FromBaseImage(baseImageName))
			h.AssertNil(t, err)

			id, err := img.Identifier()
			h.AssertNil(t, err)

			inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), id.String())
			h.AssertNil(t, err)
			labelValue := inspect.Config.Labels["existingLabel"]
			h.AssertEq(t, labelValue, "existingValue")
		})

		when("the image has been modified and saved", func() {
			it.After(func() {
				h.AssertNil(t, h.DockerRmi(dockerClient, repoName))
			})

			it("returns the new image ID", func() {
				img, err := local.NewImage(repoName, dockerClient, local.FromBaseImage(baseImageName))
				h.AssertNil(t, err)

				h.AssertNil(t, img.SetLabel("new", "label"))

				h.AssertNil(t, img.Save())
				h.AssertNil(t, err)

				id, err := img.Identifier()
				h.AssertNil(t, err)

				inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), id.String())
				h.AssertNil(t, err)

				label := inspect.Config.Labels["new"]
				h.AssertEq(t, strings.TrimSpace(label), "label")
			})
		})
	})

	when("#SetLabel", func() {
		var (
			img           imgutil.Image
			repoName      = newTestImageName()
			baseImageName = newTestImageName()
		)

		it.After(func() {
			h.AssertNil(t, h.DockerRmi(dockerClient, repoName, baseImageName))
		})

		when("base image has labels", func() {
			it("sets label and saves label to docker daemon", func() {
				var err error

				baseImage, err := local.NewImage(baseImageName, dockerClient)
				h.AssertNil(t, err)

				h.AssertNil(t, baseImage.SetLabel("some-key", "some-value"))
				h.AssertNil(t, baseImage.Save())

				img, err = local.NewImage(repoName, dockerClient, local.FromBaseImage(baseImageName))
				h.AssertNil(t, err)

				h.AssertNil(t, img.SetLabel("somekey", "new-val"))

				label, err := img.Label("somekey")
				h.AssertNil(t, err)
				h.AssertEq(t, label, "new-val")

				h.AssertNil(t, img.Save())

				inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), repoName)
				h.AssertNil(t, err)
				label = inspect.Config.Labels["somekey"]
				h.AssertEq(t, strings.TrimSpace(label), "new-val")
			})
		})

		when("no labels exists", func() {
			it("sets label and saves label to docker daemon", func() {
				var err error
				baseImage, err := local.NewImage(baseImageName, dockerClient)
				h.AssertNil(t, err)

				h.AssertNil(t, baseImage.SetCmd("/usr/bin/run"))
				h.AssertNil(t, baseImage.Save())

				img, err = local.NewImage(repoName, dockerClient, local.FromBaseImage(baseImageName))
				h.AssertNil(t, err)

				h.AssertNil(t, img.SetLabel("somekey", "new-val"))

				label, err := img.Label("somekey")
				h.AssertNil(t, err)
				h.AssertEq(t, label, "new-val")

				h.AssertNil(t, img.Save())

				inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), repoName)
				h.AssertNil(t, err)

				label = inspect.Config.Labels["somekey"]
				h.AssertEq(t, strings.TrimSpace(label), "new-val")
			})
		})
	})

	when("#SetEnv", func() {
		var repoName = newTestImageName()

		it.After(func() {
			h.AssertNil(t, h.DockerRmi(dockerClient, repoName))
		})

		it("sets the environment", func() {
			img, err := local.NewImage(repoName, dockerClient)
			h.AssertNil(t, err)

			err = img.SetEnv("ENV_KEY", "ENV_VAL")
			h.AssertNil(t, err)

			h.AssertNil(t, img.Save())

			inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), repoName)
			h.AssertNil(t, err)

			h.AssertContains(t, inspect.Config.Env, "ENV_KEY=ENV_VAL")
		})
	})

	when("#SetWorkingDir", func() {
		var repoName = newTestImageName()

		it.After(func() {
			h.AssertNil(t, h.DockerRmi(dockerClient, repoName))
		})

		it("sets the environment", func() {
			img, err := local.NewImage(repoName, dockerClient)
			h.AssertNil(t, err)

			err = img.SetWorkingDir("/some/work/dir")
			h.AssertNil(t, err)

			h.AssertNil(t, img.Save())

			inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), repoName)
			h.AssertNil(t, err)

			h.AssertEq(t, inspect.Config.WorkingDir, "/some/work/dir")
		})
	})

	when("#SetEntrypoint", func() {
		var repoName = newTestImageName()

		it.After(func() {
			h.AssertNil(t, h.DockerRmi(dockerClient, repoName))
		})

		it("sets the entrypoint", func() {
			img, err := local.NewImage(repoName, dockerClient)
			h.AssertNil(t, err)

			err = img.SetEntrypoint("some", "entrypoint")
			h.AssertNil(t, err)

			h.AssertNil(t, img.Save())

			inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), repoName)
			h.AssertNil(t, err)

			h.AssertEq(t, []string(inspect.Config.Entrypoint), []string{"some", "entrypoint"})
		})
	})

	when("#SetCmd", func() {
		var repoName = newTestImageName()

		it.After(func() {
			h.AssertNil(t, h.DockerRmi(dockerClient, repoName))
		})

		it("sets the cmd", func() {
			img, err := local.NewImage(repoName, dockerClient)
			h.AssertNil(t, err)

			err = img.SetCmd("some", "cmd")
			h.AssertNil(t, err)

			h.AssertNil(t, img.Save())

			inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), repoName)
			h.AssertNil(t, err)

			h.AssertEq(t, []string(inspect.Config.Cmd), []string{"some", "cmd"})
		})
	})

	when("#Rebase", func() {
		when("image exists", func() {
			var (
				oldBase, oldTopLayer, newBase, origID string
				origNumLayers                         int
				repoName                              = newTestImageName()
			)

			it.Before(func() {
				// new base image
				newBase = "pack-newbase-test-" + h.RandString(10)
				newBaseImage, err := local.NewImage(newBase, dockerClient)
				h.AssertNil(t, err)

				newBaseLayer1Path, err := h.CreateSingleFileBaseLayerTar("/base.txt", "new-base", daemonOS)
				h.AssertNil(t, err)
				defer os.Remove(newBaseLayer1Path)

				newBaseLayer2Path, err := h.CreateSingleFileLayerTar("/otherfile.txt", "text-new-base", daemonOS)
				h.AssertNil(t, err)
				defer os.Remove(newBaseLayer2Path)

				h.AssertNil(t, newBaseImage.AddLayer(newBaseLayer1Path))
				h.AssertNil(t, newBaseImage.AddLayer(newBaseLayer2Path))

				h.AssertNil(t, newBaseImage.Save())

				// old base image
				oldBase = "pack-oldbase-test-" + h.RandString(10)
				oldBaseImage, err := local.NewImage(oldBase, dockerClient)
				h.AssertNil(t, err)

				oldBaseLayer1Path, err := h.CreateSingleFileBaseLayerTar("/base.txt", "old-base", daemonOS)
				h.AssertNil(t, err)
				defer os.Remove(oldBaseLayer1Path)

				oldBaseLayer2Path, err := h.CreateSingleFileLayerTar("/otherfile.txt", "text-old-base", daemonOS)
				h.AssertNil(t, err)
				defer os.Remove(oldBaseLayer2Path)

				h.AssertNil(t, oldBaseImage.AddLayer(oldBaseLayer1Path))
				h.AssertNil(t, oldBaseImage.AddLayer(oldBaseLayer2Path))

				h.AssertNil(t, oldBaseImage.Save())

				inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), oldBase)
				h.AssertNil(t, err)
				oldTopLayer = inspect.RootFS.Layers[len(inspect.RootFS.Layers)-1]

				// original image
				origImage, err := local.NewImage(repoName, dockerClient, local.FromBaseImage(oldBase))
				h.AssertNil(t, err)

				imgLayer1Path, err := h.CreateSingleFileLayerTar("/myimage.txt", "text-from-image", daemonOS)
				h.AssertNil(t, err)
				defer os.Remove(imgLayer1Path)

				imgLayer2Path, err := h.CreateSingleFileLayerTar("/myimage2.txt", "text-from-image", daemonOS)
				h.AssertNil(t, err)
				defer os.Remove(imgLayer2Path)

				h.AssertNil(t, origImage.AddLayer(imgLayer1Path))
				h.AssertNil(t, origImage.AddLayer(imgLayer2Path))

				h.AssertNil(t, origImage.Save())

				inspect, _, err = dockerClient.ImageInspectWithRaw(context.TODO(), repoName)
				h.AssertNil(t, err)
				origNumLayers = len(inspect.RootFS.Layers)
				origID = inspect.ID
			})

			it.After(func() {
				h.AssertNil(t, h.DockerRmi(dockerClient, repoName, oldBase, newBase, origID))
			})

			it("switches the base", func() {
				// Before
				txt, err := h.CopySingleFileFromImage(dockerClient, repoName, "/base.txt")
				h.AssertNil(t, err)
				h.AssertEq(t, txt, "old-base")

				// Run rebase
				img, err := local.NewImage(repoName, dockerClient, local.FromBaseImage(repoName))
				h.AssertNil(t, err)
				newBaseImg, err := local.NewImage(newBase, dockerClient, local.FromBaseImage(newBase))
				h.AssertNil(t, err)
				err = img.Rebase(oldTopLayer, newBaseImg)
				h.AssertNil(t, err)

				h.AssertNil(t, img.Save())

				// After
				expected := map[string]string{
					"/base.txt":      "new-base",
					"/otherfile.txt": "text-new-base",
					"/myimage.txt":   "text-from-image",
					"/myimage2.txt":  "text-from-image",
				}
				ctrID, err := h.CreateContainer(dockerClient, repoName)
				h.AssertNil(t, err)
				defer dockerClient.ContainerRemove(context.Background(), ctrID, types.ContainerRemoveOptions{})
				for filename, expectedText := range expected {
					actualText, err := h.CopySingleFileFromContainer(dockerClient, ctrID, filename)
					h.AssertNil(t, err)
					h.AssertEq(t, actualText, expectedText)
				}

				// Final Image should have same number of layers as initial image
				inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), repoName)
				h.AssertNil(t, err)
				numLayers := len(inspect.RootFS.Layers)
				h.AssertEq(t, numLayers, origNumLayers)
			})
		})
	})

	when("#TopLayer", func() {
		when("image exists", func() {
			var (
				expectedTopLayer string
				repoName         = newTestImageName()
			)
			it.Before(func() {
				existingImage, err := local.NewImage(
					repoName,
					dockerClient,
				)
				h.AssertNil(t, err)

				layer1Path, err := h.CreateSingleFileBaseLayerTar("/base.txt", "old-base", daemonOS)
				h.AssertNil(t, err)
				layer2Path, err := h.CreateSingleFileLayerTar("/otherfile.txt", "text-old-base", daemonOS)
				h.AssertNil(t, err)

				h.AssertNil(t, existingImage.AddLayer(layer1Path))
				h.AssertNil(t, existingImage.AddLayer(layer2Path))

				h.AssertNil(t, existingImage.Save())

				inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), repoName)
				h.AssertNil(t, err)
				expectedTopLayer = inspect.RootFS.Layers[len(inspect.RootFS.Layers)-1]
			})

			it.After(func() {
				h.AssertNil(t, h.DockerRmi(dockerClient, repoName))
			})

			it("returns the digest for the top layer (useful for rebasing)", func() {
				img, err := local.NewImage(repoName, dockerClient, local.FromBaseImage(repoName))
				h.AssertNil(t, err)

				actualTopLayer, err := img.TopLayer()
				h.AssertNil(t, err)

				h.AssertEq(t, actualTopLayer, expectedTopLayer)
			})
		})

		when("image has no layers", func() {
			it("returns error", func() {
				img, err := local.NewImage(newTestImageName(), dockerClient)
				h.AssertNil(t, err)

				_, err = img.TopLayer()
				h.AssertError(t, err, "has no layers")
			})
		})
	})

	when("#AddLayer", func() {
		when("empty image", func() {
			var repoName = newTestImageName()

			it("appends a layer", func() {
				img, err := local.NewImage(repoName, dockerClient)
				h.AssertNil(t, err)

				newLayerPath, err := h.CreateSingleFileBaseLayerTar("/new-layer.txt", "new-layer", daemonOS)
				h.AssertNil(t, err)
				defer os.Remove(newLayerPath)

				h.AssertNil(t, img.AddLayer(newLayerPath))

				h.AssertNil(t, img.Save())
				defer h.DockerRmi(dockerClient, repoName)

				output, err := h.CopySingleFileFromImage(dockerClient, repoName, "new-layer.txt")
				h.AssertNil(t, err)
				h.AssertEq(t, output, "new-layer")
			})
		})

		when("base image exists", func() {
			var (
				repoName      = newTestImageName()
				baseImageName = newTestImageName()
			)

			it("appends a layer", func() {
				baseImage, err := local.NewImage(baseImageName, dockerClient)
				h.AssertNil(t, err)

				oldLayerPath, err := h.CreateSingleFileBaseLayerTar("/old-layer.txt", "old-layer", daemonOS)
				h.AssertNil(t, err)
				defer os.Remove(oldLayerPath)

				h.AssertNil(t, baseImage.AddLayer(oldLayerPath))

				h.AssertNil(t, baseImage.Save())
				defer h.DockerRmi(dockerClient, baseImageName)

				img, err := local.NewImage(
					repoName,
					dockerClient,
					local.FromBaseImage(baseImageName),
				)
				h.AssertNil(t, err)

				newLayerPath, err := h.CreateSingleFileLayerTar("/new-layer.txt", "new-layer", daemonOS)
				h.AssertNil(t, err)
				defer os.Remove(newLayerPath)

				h.AssertNil(t, img.AddLayer(newLayerPath))

				h.AssertNil(t, img.Save())
				defer h.DockerRmi(dockerClient, repoName)

				output, err := h.CopySingleFileFromImage(dockerClient, repoName, "old-layer.txt")
				h.AssertNil(t, err)
				h.AssertEq(t, output, "old-layer")

				output, err = h.CopySingleFileFromImage(dockerClient, repoName, "new-layer.txt")
				h.AssertNil(t, err)
				h.AssertEq(t, output, "new-layer")
			})
		})
	})

	when("#AddLayerWithDiffID", func() {
		var (
			repoName        = newTestImageName()
			existingImageID string
		)

		it.Before(func() {
			existingImage, err := local.NewImage(repoName, dockerClient)
			h.AssertNil(t, err)

			oldLayerPath, err := h.CreateSingleFileBaseLayerTar("/old-layer.txt", "old-layer", daemonOS)
			h.AssertNil(t, err)
			defer os.Remove(oldLayerPath)

			h.AssertNil(t, existingImage.AddLayer(oldLayerPath))

			h.AssertNil(t, existingImage.Save())

			id, err := existingImage.Identifier()
			h.AssertNil(t, err)

			existingImageID = id.String()
		})

		it.After(func() {
			h.AssertNil(t, h.DockerRmi(dockerClient, repoName, existingImageID))
		})

		it("appends a layer", func() {
			var err error

			img, err := local.NewImage(
				repoName, dockerClient,
				local.FromBaseImage(repoName),
				local.WithPreviousImage(repoName),
			)
			h.AssertNil(t, err)

			newLayerPath, err := h.CreateSingleFileLayerTar("/new-layer.txt", "new-layer", daemonOS)
			h.AssertNil(t, err)
			defer os.Remove(newLayerPath)

			diffID, err := h.FileDiffID(newLayerPath)
			h.AssertNil(t, err)

			h.AssertNil(t, img.AddLayerWithDiffID(newLayerPath, diffID))
			h.AssertNil(t, img.Save())

			output, err := h.CopySingleFileFromImage(dockerClient, repoName, "old-layer.txt")
			h.AssertNil(t, err)
			h.AssertEq(t, output, "old-layer")

			output, err = h.CopySingleFileFromImage(dockerClient, repoName, "new-layer.txt")
			h.AssertNil(t, err)
			h.AssertEq(t, output, "new-layer")
		})
	})

	when("#GetLayer", func() {
		when("the layer exists", func() {
			var repoName = newTestImageName()

			it.Before(func() {
				var err error

				existingImage, err := local.NewImage(repoName, dockerClient)
				h.AssertNil(t, err)

				layerPath, err := h.CreateSingleFileBaseLayerTar("/file.txt", "file-contents", daemonOS)
				h.AssertNil(t, err)
				defer os.Remove(layerPath)

				h.AssertNil(t, existingImage.AddLayer(layerPath))

				h.AssertNil(t, existingImage.Save())
			})

			it.After(func() {
				h.AssertNil(t, h.DockerRmi(dockerClient, repoName))
			})

			when("the layer exists", func() {
				it("returns a layer tar", func() {
					img, err := local.NewImage(repoName, dockerClient, local.FromBaseImage(repoName))
					h.AssertNil(t, err)

					topLayer, err := img.TopLayer()
					h.AssertNil(t, err)

					r, err := img.GetLayer(topLayer)
					h.AssertNil(t, err)
					tr := tar.NewReader(r)

					// continue until reader is at matching file
					for {
						header, err := tr.Next()
						h.AssertNil(t, err)

						if strings.HasSuffix(header.Name, "/file.txt") {
							break
						}
					}

					contents := make([]byte, len("file-contents"))
					_, err = tr.Read(contents)
					if err != io.EOF {
						t.Fatalf("expected end of file: %x", err)
					}
					h.AssertEq(t, string(contents), "file-contents")
				})
			})

			when("the layer does not exist", func() {
				it("returns an error", func() {
					img, err := local.NewImage(repoName, dockerClient, local.FromBaseImage(repoName))
					h.AssertNil(t, err)
					h.AssertNil(t, err)
					_, err = img.GetLayer("not-exist")
					h.AssertError(
						t,
						err,
						fmt.Sprintf("image '%s' does not contain layer with diff ID 'not-exist'", repoName),
					)
				})
			})
		})

		when("image does NOT exist", func() {
			it("returns error", func() {
				image, err := local.NewImage(newTestImageName(), dockerClient)
				h.AssertNil(t, err)

				readCloser, err := image.GetLayer(h.RandString(10))
				h.AssertNil(t, readCloser)
				h.AssertError(t, err, "No such image")
			})
		})
	})

	when("#ReuseLayer", func() {
		var (
			layer1SHA string
			layer2SHA string
			prevName  = newTestImageName()
			repoName  = newTestImageName()
		)
		it.Before(func() {
			prevImage, err := local.NewImage(
				prevName,
				dockerClient,
			)
			h.AssertNil(t, err)

			layer1Path, err := h.CreateSingleFileBaseLayerTar("/layer-1.txt", "old-layer-1", daemonOS)
			h.AssertNil(t, err)
			defer os.Remove(layer1Path)

			layer2Path, err := h.CreateSingleFileLayerTar("/layer-2.txt", "old-layer-2", daemonOS)
			h.AssertNil(t, err)
			defer os.Remove(layer2Path)

			h.AssertNil(t, prevImage.AddLayer(layer1Path))
			h.AssertNil(t, prevImage.AddLayer(layer2Path))

			h.AssertNil(t, prevImage.Save())

			inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), prevName)
			h.AssertNil(t, err)

			layer1SHA = inspect.RootFS.Layers[0]
			layer2SHA = inspect.RootFS.Layers[1]
		})

		it.After(func() {
			h.AssertNil(t, h.DockerRmi(dockerClient, repoName, prevName))
		})

		it("reuses a layer", func() {
			img, err := local.NewImage(
				repoName,
				dockerClient,
				local.WithPreviousImage(prevName),
			)
			h.AssertNil(t, err)

			newBaseLayerPath, err := h.CreateSingleFileBaseLayerTar("/new-base.txt", "base-content", daemonOS)
			h.AssertNil(t, err)
			defer os.Remove(newBaseLayerPath)

			h.AssertNil(t, img.AddLayer(newBaseLayerPath))

			err = img.ReuseLayer(layer2SHA)
			h.AssertNil(t, err)

			h.AssertNil(t, img.Save())

			output, err := h.CopySingleFileFromImage(dockerClient, repoName, "layer-2.txt")
			h.AssertNil(t, err)
			h.AssertEq(t, output, "old-layer-2")

			// Confirm layer-1.txt does not exist
			_, err = h.CopySingleFileFromImage(dockerClient, repoName, "layer-1.txt")
			h.AssertMatch(t, err.Error(), regexp.MustCompile(`Error: No such container:path: .*:layer-1.txt`))
		})

		it("does not download the old image if layers are directly above (performance)", func() {
			img, err := local.NewImage(
				repoName,
				dockerClient,
				local.WithPreviousImage(prevName),
			)
			h.AssertNil(t, err)

			err = img.ReuseLayer(layer1SHA)
			h.AssertNil(t, err)

			h.AssertNil(t, img.Save())

			output, err := h.CopySingleFileFromImage(dockerClient, repoName, "layer-1.txt")
			h.AssertNil(t, err)
			h.AssertEq(t, output, "old-layer-1")

			// Confirm layer-2.txt does not exist
			_, err = h.CopySingleFileFromImage(dockerClient, repoName, "layer-2.txt")
			h.AssertMatch(t, err.Error(), regexp.MustCompile(`Error: No such container:path: .*:layer-2.txt`))
		})
	})

	when("#Save", func() {
		when("image is valid", func() {
			var (
				img      imgutil.Image
				origID   string
				tarPath  string
				repoName = newTestImageName()
			)

			it.Before(func() {
				oldImage, err := local.NewImage(repoName, dockerClient)
				h.AssertNil(t, err)

				h.AssertNil(t, oldImage.SetLabel("mykey", "oldValue"))
				h.AssertNil(t, oldImage.Save())

				origID = h.ImageID(t, repoName)

				img, err = local.NewImage(repoName, dockerClient, local.FromBaseImage(runnableBaseImageName))
				h.AssertNil(t, err)

				tarPath, err = h.CreateSingleFileLayerTar("/new-layer.txt", "new-layer", daemonOS)
				h.AssertNil(t, err)
			})

			it.After(func() {
				h.AssertNil(t, os.Remove(tarPath))
				h.AssertNil(t, h.DockerRmi(dockerClient, repoName, origID))
			})

			it("saved image overrides image with new ID", func() {
				err := img.SetLabel("mykey", "newValue")
				h.AssertNil(t, err)

				err = img.AddLayer(tarPath)
				h.AssertNil(t, err)

				h.AssertNil(t, img.Save())

				identifier, err := img.Identifier()
				h.AssertNil(t, err)

				h.AssertEq(t, origID != identifier.String(), true)

				newImageID := h.ImageID(t, repoName)
				h.AssertNotEq(t, origID, newImageID)

				inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), identifier.String())
				h.AssertNil(t, err)
				label := inspect.Config.Labels["mykey"]
				h.AssertEq(t, strings.TrimSpace(label), "newValue")
			})

			it("zeroes times and client specific fields", func() {
				err := img.SetLabel("mykey", "newValue")
				h.AssertNil(t, err)

				err = img.AddLayer(tarPath)
				h.AssertNil(t, err)

				h.AssertNil(t, img.Save())

				inspect, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), repoName)
				h.AssertNil(t, err)

				h.AssertEq(t, inspect.Created, imgutil.NormalizedDateTime.Format(time.RFC3339))
				h.AssertEq(t, inspect.Container, "")
				h.AssertEq(t, inspect.DockerVersion, "")

				history, err := dockerClient.ImageHistory(context.TODO(), repoName)
				h.AssertNil(t, err)
				h.AssertEq(t, len(history), len(inspect.RootFS.Layers))
				for i := range inspect.RootFS.Layers {
					h.AssertEq(t, history[i].Created, imgutil.NormalizedDateTime.Unix())
				}
			})

			when("additional names are provided", func() {
				var (
					additionalRepoNames = []string{
						repoName + ":" + h.RandString(5),
						newTestImageName(),
						newTestImageName(),
					}
					successfulRepoNames = append([]string{repoName}, additionalRepoNames...)
				)

				it.After(func() {
					h.AssertNil(t, h.DockerRmi(dockerClient, additionalRepoNames...))
				})

				it("saves to multiple names", func() {
					h.AssertNil(t, img.Save(additionalRepoNames...))

					for _, n := range successfulRepoNames {
						_, _, err := dockerClient.ImageInspectWithRaw(context.TODO(), n)
						h.AssertNil(t, err)
					}
				})

				when("a single image name fails", func() {
					it("returns results with errors for those that failed", func() {
						failingName := newTestImageName() + ":🧨"

						err := img.Save(append([]string{failingName}, additionalRepoNames...)...)
						h.AssertError(t, err, fmt.Sprintf("failed to write image to the following tags: [%s:", failingName))

						saveErr, ok := err.(imgutil.SaveError)
						h.AssertEq(t, ok, true)
						h.AssertEq(t, len(saveErr.Errors), 1)
						h.AssertEq(t, saveErr.Errors[0].ImageName, failingName)
						h.AssertError(t, saveErr.Errors[0].Cause, "invalid reference format")

						for _, n := range successfulRepoNames {
							_, _, err = dockerClient.ImageInspectWithRaw(context.TODO(), n)
							h.AssertNil(t, err)
						}
					})
				})
			})
		})

		when("invalid image content for daemon", func() {
			it("returns errors from daemon", func() {
				repoName := newTestImageName()

				invalidLayerTarFile, err := ioutil.TempFile("", "daemon-error-test")
				h.AssertNil(t, err)
				defer func() { invalidLayerTarFile.Close(); os.Remove(invalidLayerTarFile.Name()) }()

				invalidLayerTarFile.Write([]byte("NOT A TAR"))
				invalidLayerPath := invalidLayerTarFile.Name()

				img, err := local.NewImage(repoName, dockerClient)
				h.AssertNil(t, err)

				err = img.AddLayer(invalidLayerPath)
				h.AssertNil(t, err)

				err = img.Save()
				h.AssertError(t, err, fmt.Sprintf("failed to write image to the following tags: [%s:", repoName))
				h.AssertError(t, err, "daemon response")
			})
		})
	})

	when("#Found", func() {
		when("it exists", func() {
			var repoName = newTestImageName()

			it.Before(func() {
				existingImage, err := local.NewImage(repoName, dockerClient)
				h.AssertNil(t, err)
				h.AssertNil(t, existingImage.Save())
			})

			it.After(func() {
				h.DockerRmi(dockerClient, repoName)
			})

			it("returns true, nil", func() {
				image, err := local.NewImage(repoName, dockerClient, local.FromBaseImage(repoName))
				h.AssertNil(t, err)

				h.AssertEq(t, image.Found(), true)
			})
		})

		when("it does not exist", func() {
			it("returns false, nil", func() {
				image, err := local.NewImage(newTestImageName(), dockerClient)
				h.AssertNil(t, err)

				h.AssertEq(t, image.Found(), false)
			})
		})
	})

	when("#Delete", func() {
		when("the image does not exist", func() {
			it("should not error", func() {
				img, err := local.NewImage("image-does-not-exist", dockerClient)
				h.AssertNil(t, err)

				h.AssertNil(t, img.Delete())
			})
		})

		when("the image does exist", func() {
			var (
				origImg  imgutil.Image
				origID   string
				repoName = newTestImageName()
			)

			it.Before(func() {
				existingImage, err := local.NewImage(repoName, dockerClient)
				h.AssertNil(t, err)
				h.AssertNil(t, existingImage.SetLabel("some", "label"))
				h.AssertNil(t, existingImage.Save())

				origImg, err = local.NewImage(repoName, dockerClient, local.FromBaseImage(repoName))
				h.AssertNil(t, err)

				origID = h.ImageID(t, repoName)
			})

			it("should delete the image", func() {
				h.AssertEq(t, origImg.Found(), true)

				h.AssertNil(t, origImg.Delete())

				img, err := local.NewImage(origID, dockerClient)
				h.AssertNil(t, err)

				h.AssertEq(t, img.Found(), false)
			})

			when("the image has been re-tagged", func() {
				const newTag = "different-tag"

				it.Before(func() {
					h.AssertNil(t, dockerClient.ImageTag(context.TODO(), origImg.Name(), newTag))

					_, err := dockerClient.ImageRemove(context.TODO(), origImg.Name(), types.ImageRemoveOptions{})
					h.AssertNil(t, err)
				})

				it("should delete the image", func() {
					h.AssertEq(t, origImg.Found(), true)

					h.AssertNil(t, origImg.Delete())

					origImg, err := local.NewImage(newTag, dockerClient)
					h.AssertNil(t, err)

					h.AssertEq(t, origImg.Found(), false)
				})
			})
		})
	})
}
