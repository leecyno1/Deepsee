package model

import (
	"path/filepath"
	"testing"
)

func TestMediaV4WrapUsesRecordDirectories(t *testing.T) {
	tests := []struct {
		name string
		in   MediaV4
		want string
	}{
		{
			name: "record image",
			in:   MediaV4{Type: "image", HardLinkType: 4, Dir1: "talker", Dir2: "2026-07", ExtraBuffer: "record-1", Name: "a.dat"},
			want: filepath.Join("msg", "attach", "talker", "2026-07", "Rec", "record", "Img", "a.dat"),
		},
		{
			name: "regular image ignores protobuf varint bytes",
			in: MediaV4{
				Type: "image", HardLinkType: 2, Dir1: "talker", Dir2: "2026-07",
				ExtraBuffer: string([]byte{0x10, 0x9d, 0x56}), Name: "a_h.dat",
			},
			want: filepath.Join("msg", "attach", "talker", "2026-07", "Img", "a_h.dat"),
		},
		{
			name: "record image reads protobuf directory",
			in: MediaV4{
				Type: "image", HardLinkType: 4, Dir1: "talker", Dir2: "2026-07",
				ExtraBuffer: string(append([]byte{0x0a, 0x10}, append([]byte("3dcf9d5877a5ca3e"), 0x10, 0xd7, 0xa3, 0x04)...)),
				Name:        "0",
			},
			want: filepath.Join("msg", "attach", "talker", "2026-07", "Rec", "3dcf9d5877a5ca3e", "Img", "0"),
		},
		{
			name: "record video",
			in:   MediaV4{Type: "video", HardLinkType: 5, Dir1: "talker", Dir2: "2026-07", ExtraBuffer: "record", Name: "a.mp4"},
			want: filepath.Join("msg", "attach", "talker", "2026-07", "Rec", "record", "V", "a.mp4"),
		},
		{
			name: "nested record file",
			in:   MediaV4{Type: "file", HardLinkType: 6, Dir1: "talker", Dir2: "2026-07", ExtraBuffer: "record/index", Name: "a.pdf"},
			want: filepath.Join("msg", "attach", "talker", "2026-07", "Rec", "record", "F", "index", "a.pdf"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Wrap().Path; got != tc.want {
				t.Fatalf("Path = %q, want %q", got, tc.want)
			}
		})
	}
}
