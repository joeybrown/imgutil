package layer_test

import (
	"archive/tar"
	"bytes"
	"fmt"
	"os"
	"testing"

	"github.com/buildpacks/imgutil/layer"
	h "github.com/buildpacks/imgutil/testhelpers"
)

func TestWindowsWriter_WriteHeader(t *testing.T) {
	t.Run("successful write", func(t *testing.T) {
		testCases := map[string]struct {
			headers      []tar.Header
			expectations []struct {
				name       string
				flag       byte
				paxRecords map[string]string
			}
		}{
			"writes required entries": {
				headers: []tar.Header{
					{
						Name:     "/cnb/my-file",
						Typeflag: tar.TypeReg,
					},
				},
				expectations: []struct {
					name       string
					flag       byte
					paxRecords map[string]string
				}{
					{
						name:       "Files/cnb/my-file",
						flag:       byte(tar.TypeReg),
						paxRecords: map[string]string{"MSWINDOWS.rawsd": layer.AdministratratorOwnerAndGroupSID},
					},
				},
			},
			"duplicate parent directories": {
				headers: []tar.Header{
					{
						Name:     "/cnb/lifecycle/first-file",
						Typeflag: tar.TypeReg,
					},
					{
						Name:     "/cnb/sibling-dir",
						Typeflag: tar.TypeDir,
					},
				},
				expectations: []struct {
					name       string
					flag       byte
					paxRecords map[string]string
				}{
					{
						name:       "Files/cnb/lifecycle",
						flag:       byte(tar.TypeDir),
						paxRecords: map[string]string(nil),
					},
					{
						name:       "Files/cnb/lifecycle/first-file",
						flag:       byte(tar.TypeReg),
						paxRecords: map[string]string{"MSWINDOWS.rawsd": layer.AdministratratorOwnerAndGroupSID},
					},
					{
						name:       "Files/cnb/sibling-dir",
						flag:       byte(tar.TypeDir),
						paxRecords: map[string]string{"MSWINDOWS.rawsd": layer.AdministratratorOwnerAndGroupSID},
					},
				},
			},
		}

		for name, testCase := range testCases {
			t.Run(name, func(t *testing.T) {
				tc := testCase

				var err error

				f, err := os.CreateTemp("", "windows-writer.tar")
				h.AssertNil(t, err)
				defer func() { f.Close(); os.Remove(f.Name()) }()

				lw := layer.NewWindowsWriter(f)

				for _, element := range tc.headers {
					h.AssertNil(t, lw.WriteHeader(&element))
				}

				h.AssertNil(t, lw.Close())

				_, err = f.Seek(0, 0)
				h.AssertNil(t, err)
				tr := tar.NewReader(f)

				th, _ := tr.Next()
				h.AssertEq(t, th.Name, "Files")
				h.AssertEq(t, th.Typeflag, byte(tar.TypeDir))
				h.AssertEq(t, th.PAXRecords, map[string]string(nil))

				th, _ = tr.Next()
				h.AssertEq(t, th.Name, "Hives")
				h.AssertEq(t, th.Typeflag, byte(tar.TypeDir))
				h.AssertEq(t, th.PAXRecords, map[string]string(nil))

				th, _ = tr.Next()
				h.AssertEq(t, th.Name, "Files/cnb")
				h.AssertEq(t, th.Typeflag, byte(tar.TypeDir))
				h.AssertEq(t, th.PAXRecords, map[string]string(nil))

				for _, expected := range tc.expectations {
					th, _ = tr.Next()
					h.AssertEq(t, th.Name, expected.name)
					h.AssertEq(t, th.Typeflag, expected.flag)
					h.AssertEq(t, th.PAXRecords, expected.paxRecords)
				}

				_, err = tr.Next()
				h.AssertError(t, err, "EOF")
			})
		}
	})

	t.Run("invalid header name", func(t *testing.T) {
		testCases := map[string]struct {
			name   string
			flag   byte
			errMsg string
		}{
			"windows path": {
				name:   `c:\windows-path.txt`,
				flag:   tar.TypeReg,
				errMsg: `invalid header name: must be absolute, posix path: c:\windows-path.txt`,
			},
			"lonely file": {
				name:   `\lonelyfile`,
				flag:   tar.TypeDir,
				errMsg: `invalid header name: must be absolute, posix path: \lonelyfile`,
			},
			"relative": {
				name:   "Files/cnb/lifecycle/first-file",
				flag:   tar.TypeDir,
				errMsg: `invalid header name: must be absolute, posix path: Files/cnb/lifecycle/first-file`,
			},
		}

		for name, testCase := range testCases {
			t.Run(name, func(t *testing.T) {
				tc := testCase

				lw := layer.NewWindowsWriter(&bytes.Buffer{})
				h.AssertError(t, lw.WriteHeader(&tar.Header{
					Name:     tc.name,
					Typeflag: tc.flag,
				}), tc.errMsg)
			})
		}
	})

	t.Run("PAX permissions", func(t *testing.T) {

		const headerName = "/cnb/my-file"

		testCases := map[string]struct {
			header             tar.Header
			expectedPaxRecords map[string]string
		}{
			"admin-owned entries": {
				header: tar.Header{
					Name:     headerName,
					Typeflag: tar.TypeReg,
					Uid:      0,
					Gid:      0,
				},
				expectedPaxRecords: map[string]string{"MSWINDOWS.rawsd": layer.AdministratratorOwnerAndGroupSID},
			},
			"user-owned entries": {
				header: tar.Header{
					Name:     headerName,
					Typeflag: tar.TypeReg,
					Uid:      1000,
					Gid:      1000,
				},
				expectedPaxRecords: map[string]string{"MSWINDOWS.rawsd": layer.UserOwnerAndGroupSID},
			},
			"existing security descriptor PAX record": {
				header: tar.Header{
					Name:       headerName,
					Typeflag:   tar.TypeReg,
					PAXRecords: map[string]string{"MSWINDOWS.rawsd": "bar"},
				},
				expectedPaxRecords: map[string]string{"MSWINDOWS.rawsd": "bar"},
			},
		}

		for name, testCase := range testCases {
			t.Run(name, func(t *testing.T) {
				tc := testCase

				var err error

				f, err := os.CreateTemp("", "windows-writer.tar")
				h.AssertNil(t, err)
				defer func() { f.Close(); os.Remove(f.Name()) }()

				lw := layer.NewWindowsWriter(f)

				h.AssertNil(t, lw.WriteHeader(&tc.header))

				h.AssertNil(t, lw.Close())

				_, err = f.Seek(0, 0)
				h.AssertNil(t, err)

				tr := tar.NewReader(f)

				_, err = tr.Next() // Files
				h.AssertNil(t, err)
				_, err = tr.Next() // Hives
				h.AssertNil(t, err)
				_, err = tr.Next() // Files/cnb
				h.AssertNil(t, err)
				th, err := tr.Next()
				h.AssertNil(t, err)
				h.AssertEq(t, th.Name, fmt.Sprintf("Files%s", headerName))
				h.AssertEq(t, th.PAXRecords, tc.expectedPaxRecords)
			})
		}
	})
}

func TestWindowsWriter_Close(t *testing.T) {
	t.Run("writes required parent dirs on empty layer", func(t *testing.T) {
		var err error

		f, err := os.CreateTemp("", "windows-writer.tar")
		h.AssertNil(t, err)
		defer func() { f.Close(); os.Remove(f.Name()) }()

		lw := layer.NewWindowsWriter(f)

		err = lw.Close()
		h.AssertNil(t, err)

		f.Seek(0, 0)
		tr := tar.NewReader(f)

		th, _ := tr.Next()
		h.AssertEq(t, th.Name, "Files")
		h.AssertEq(t, th.Typeflag, byte(tar.TypeDir))

		th, _ = tr.Next()
		h.AssertEq(t, th.Name, "Hives")
		h.AssertEq(t, th.Typeflag, byte(tar.TypeDir))

		_, err = tr.Next()
		h.AssertError(t, err, "EOF")
	})
}
