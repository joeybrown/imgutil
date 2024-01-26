package fakes_test

import (
	"archive/tar"
	"fmt"
	"path/filepath"

	"os"
	"sort"
	"testing"

	"github.com/buildpacks/imgutil"
	"github.com/buildpacks/imgutil/fakes"
	h "github.com/buildpacks/imgutil/testhelpers"
)

var localTestRegistry *h.DockerRegistry

func newRepoName() string {
	return "test-image-" + h.RandString(10)
}

func TestFakeImage(t *testing.T) {
	t.Run("NewImage", func(t *testing.T) {
		t.Run("implements imgutil.Image", func(t *testing.T) {
			t.Parallel()
			var _ imgutil.Image = fakes.NewImage("", "", nil)
		})
	})

	t.Run("SavedNames", func(t *testing.T) {
		testCases := map[string]struct {
			validRepoNames []string
			badRepoName    string
		}{
			"returns a list of saved names": {
				validRepoNames: []string{
					newRepoName(),
					newRepoName(),
					newRepoName(),
				},
			},
			"returns a list of saved names with errors": {
				validRepoNames: []string{
					newRepoName(),
					newRepoName(),
					newRepoName(),
				},
				badRepoName: newRepoName() + ":🧨",
			},
		}

		for name, testCase := range testCases {
			t.Run(name, func(t *testing.T) {
				tc := testCase
				t.Parallel()

				image := fakes.NewImage(tc.validRepoNames[0], "", nil)
				additionalNames := tc.validRepoNames[1:]

				if tc.badRepoName != "" {
					additionalNames = append(additionalNames, tc.badRepoName)
				}

				err := image.Save(additionalNames...)
				saveErr, ok := err.(imgutil.SaveError)

				names := image.SavedNames()
				h.AssertContains(t, names, tc.validRepoNames...)

				if tc.badRepoName == "" {
					h.AssertEq(t, ok, false)
					h.AssertEq(t, len(saveErr.Errors), 0)
				} else {
					h.AssertEq(t, ok, true)
					h.AssertEq(t, len(saveErr.Errors), 1)
					h.AssertEq(t, saveErr.Errors[0].ImageName, tc.badRepoName)
					h.AssertError(t, saveErr.Errors[0].Cause, "could not parse reference")
					h.AssertDoesNotContain(t, names, tc.badRepoName)
				}
			})
		}
	})

	t.Run("FindLayerWithPath", func(t *testing.T) {
		var (
			image      *fakes.Image
			layer1Path string
			layer2Path string
		)

		var err error

		image = fakes.NewImage("some-image", "", nil)

		layer1Path, err = createLayerTar(map[string]string{})
		h.AssertNil(t, err)

		err = image.AddLayer(layer1Path)
		h.AssertNil(t, err)

		layer2Path, err = createLayerTar(map[string]string{
			"/layer2/file1":     "file-1-contents",
			"/layer2/file2":     "file-2-contents",
			"/layer2/some.toml": "[[something]]",
		})
		h.AssertNil(t, err)

		err = image.AddLayer(layer2Path)
		h.AssertNil(t, err)

		t.Run("should list out contents when path not found in image", func(t *testing.T) {
			t.Parallel()
			defer func() {
				os.RemoveAll(layer1Path)
				os.RemoveAll(layer2Path)
			}()

			_, err := image.FindLayerWithPath("/non-existent/file")
			h.AssertError(t, err, fmt.Sprintf(`could not find '/non-existent/file' in any layer.

Layers
-------
%s
  (empty)

%s
  - [F] /layer2/file1
  - [F] /layer2/file2
  - [F] /layer2/some.toml
`,
				filepath.Base(layer1Path),
				filepath.Base(layer2Path)),
			)
		})
	})

	t.Run("AnnotateRefName", func(t *testing.T) {
		t.Run("annotates the image with the given ref name", func(t *testing.T) {
			t.Parallel()
			var repoName = newRepoName()
			image := fakes.NewImage(repoName, "", nil)
			err := image.AnnotateRefName("my-tag")
			h.AssertNil(t, err)

			err = image.Save()
			h.AssertNil(t, err)

			annotations := image.SavedAnnotations()
			refName, err := image.GetAnnotateRefName()
			h.AssertNil(t, err)

			h.AssertEq(t, annotations["org.opencontainers.image.ref.name"], refName)
		})
	})
}

func createLayerTar(contents map[string]string) (string, error) {
	file, err := os.CreateTemp("", "layer-*.tar")
	if err != nil {
		return "", nil
	}
	defer file.Close()

	tw := tar.NewWriter(file)

	var paths []string
	for k := range contents {
		paths = append(paths, k)
	}
	sort.Strings(paths)

	for _, path := range paths {
		txt := contents[path]

		if err := tw.WriteHeader(&tar.Header{Name: path, Size: int64(len(txt)), Mode: 0644}); err != nil {
			return "", err
		}
		if _, err := tw.Write([]byte(txt)); err != nil {
			return "", err
		}
	}

	if err := tw.Close(); err != nil {
		return "", err
	}

	return file.Name(), nil
}
