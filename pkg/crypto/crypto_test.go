// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package crypto

import (
	"crypto/md5"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateRandomKey(t *testing.T) {
	text := randomKey()
	assert.NotEmpty(t, text)
	t.Logf("GenerateRandomKey: %s\n", text) // ONRhfKsUOHoF8iVd
}

func TestReverseString(t *testing.T) {
	type args struct {
		str string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "正常",
			args: args{
				str: "123456789",
			},
			want: "987654321",
		},
		{
			name: "回文",
			args: args{
				str: "123321",
			},
			want: "123321",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, reverseString(tt.args.str), "ReverseString(%v)", tt.args.str)
		})
	}
}

func TestRsaEncryptFixedWidth(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "完整长度向量兼容",
			input: "0123456789abcdef",
			want:  "ac744caab466f1fd5a75228365d15df7add288e8982a2c8f80da5f8f22d54834b5e1eab04dde9dac7341851a98de37463d8ba01d9e9c4d2c546c0f948e3163c667785a1b9c4f160305b0fb1ec3b698de0c704719b2fbc469582654b9c2595317dc40c3ecee32baa6d9753970b66667c01bde9ad34048fa8b2b0f628e56a5bf49",
		},
		{
			name:  "保留前导零字节",
			input: "0000000000000115",
			want:  "00b8dc27daf2a6600285277504f7c39cacad3f0240fdb6371dfba07c2f72fd8a012af97b3cf86ed31a3254411eabd5f2911f2b9d5200dd24a4d9acba89b022c9face11f2927bbd07dc274395ed16b97298a3a1d9f8706f0ee40ad42fc6fb616fdaf5039ed46551377f7ead7b8671db1787fb5fd6c542d9d99475b24bbf25fd76",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 固定公开样例覆盖随机 WEAPI 密钥可能产生的前导零密文。
			got, err := RsaEncrypt(tt.input, publicKey)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 256 {
				t.Fatalf("RSA 密文长度 = %d，期望 256", len(got))
			}
			if got != tt.want {
				t.Fatalf("RSA 密文 = %s，期望 %s", got, tt.want)
			}
		})
	}
}

func TestWeApiEncrypt(t *testing.T) {
	type args struct {
		object interface{}
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "正常",
			args: args{
				object: map[string]string{"phone": "188********", "ctcode": "86"},
			},
			want:    map[string]string{},
			wantErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WeApiEncrypt(tt.args.object)
			assert.NoError(t, err)
			t.Logf("data:%+v\n", got)
			// assert.Equalf(t, tt.want, got, "weapi(%v)", tt.args.object)
		})
	}
}

func TestEApiDecrypt(t *testing.T) {
	type args struct {
		encode string
		data   string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "test request",
			args: args{
				encode: "hex",
				data:   "1BDAA66BB859333CCCE0A53AE6D1E6E61F5C1663DE05CFFB8C87BCE2FDC6F9ECAB1F5341B2FBCB5CBBACDA665D6F1A10B007189F44A13DB2463BB3EBF2639CF10A3E14D47E97975942FF626F17CE4A658E17F19C52EDACCB199F262EA09723E644C46E3880B4754AE1A2A1F4712268C52AEA6F5D0158780D82BDC30C930756181972480BE18A2ECD68A276C68E5214491F2323B3C87ECA2AF9532A4F483D55B8C5187D558AF5699D2C2437C1D98CB5AD7B90402CCDB12DF950521A86D854646BF8422708A649C1B8B752AF70AD5B3868F939FD0E9BEAA8BAE0D05BB0D4D88BE1A6BFAA8F5BBECD6F92368480E657D2200F8ACE7740ACAAA5634297D6661704EE7F74779E833DF2241939FC60C5D92569E31285E4F4A4F737CC8E89316DE7BBC8FB99E94B87DC05C190EA228637B2C0D182152BFAC603EF671A9A0B2F907D98F30E8A4614F236B3ED78392F039EDAD3C3CE5A856EE51BCDE2173F428CD1BB0239",
			},
			want: "/api/music/partner/work/evaluate-36cd479b6b5-{\"taskId\":\"185640294\",\"workId\":\"1312207\",\"score\":\"3\",\"tags\":\"3-C-1\",\"customTags\":\"[]\",\"comment\":\"\",\"extraResource\":\"true\",\"syncYunCircle\":\"false\",\"syncComment\":\"true\",\"extraScore\":\"{\\\"1\\\":3,\\\"2\\\":2,\\\"3\\\":4}\",\"source\":\"mp-music-partner\",\"header\":\"{}\",\"e_r\":true}-36cd479b6b5-f891fb9aa53a9b84280a53c43ff84de8",
			wantErr: func(t assert.TestingT, err error, msg ...interface{}) bool {
				if err != nil {
					t.Errorf("%s", msg)
					return false
				}
				return true
			},
		},
		{
			name: "test response",
			args: args{
				encode: "hex",
				data:   "DCC52B3013E9B66C038F8E027E580ECEDF84E0F44CB93FC365BED7B646A9BC08",
			},
			want: `{"code":200,"data":true}`,
			wantErr: func(t assert.TestingT, err error, msg ...interface{}) bool {
				if err != nil {
					t.Errorf("%s", msg)
					return false
				}
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EApiDecrypt(tt.args.data, "hex")
			if !tt.wantErr(t, err, fmt.Sprintf("EApiDecrypt(%v)", tt.args.data)) {
				return
			}
			t.Logf("data: %+v\n", string(got))
			t.Logf("abcd: %+v\n", tt.want)
			assert.Equalf(t, tt.want, string(got), "EApiDecrypt(%v)", tt.args.data)
		})
	}
}

func TestEApiEncrypt(t *testing.T) {
	type args struct {
		url    string
		object interface{}
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "sample",
			args: args{"/test/url", "test value"},
			want: map[string]string{"params": "E556EA4892989E4A1B98043B56CD3C77C6DBE3D0261A0FA8ACF45E2882DBABFD13F52E05D9EF39C101A7A46DD0E0CD0979A2DD9CE30975861F6F4E86855FE00AD841C36BA90177218D0D8D32A54A0DC4"},
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				if err != nil {
					t.Errorf("err: %v args: %s", err, i)
					return false
				}
				return true
			},
		},
		// {
		// 	name: "无url参数情况",
		// 	args: args{"", `{"code":200,"data":true}`},
		// 	want: map[string]string{"params": "DCC52B3013E9B66C038F8E027E580ECEDF84E0F44CB93FC365BED7B646A9BC08"},
		// 	wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
		// 		if err != nil {
		// 			t.Errorf("err: %v args: %s", err, i)
		// 			return false
		// 		}
		// 		return true
		// 	},
		// },
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EApiEncrypt(tt.args.url, tt.args.object)
			if !tt.wantErr(t, err, fmt.Sprintf("EApiEncrypt(%v, %v)", tt.args.url, tt.args.object)) {
				return
			}
			assert.Equalf(t, tt.want, got, "EApiEncrypt(%v, %v)", tt.args.url, tt.args.object)
		})
	}
}

func TestHex(t *testing.T) {
	var (
		url    = "/api/sms/captcha/sent"
		data   = `{"deviceId":"4cdb39bf34a848781b89663e1e546b8b","os":"OSX","cellphone":"188********","header":"{\"os\":\"osx\",\"appver\":\"2.3.17\",\"deviceId\":\"7A8EB581-E60B-5230-BB5B-E6DAB1FBFA62%7C5FD718A3-0602-4389-B612-EBEFAA7F108B\",\"requestId\":\"93487028\",\"clientSign\":\"\",\"osver\":\"%E7%89%88%E6%9C%AC12.6%EF%BC%88%E7%89%88%E5%8F%B721G115%EF%BC%89\",\"Nm-GCore-Status\":\"1\",\"MConfig-Info\":\"{\\\"IuRPVVmc3WWul9fT\\\":{\\\"version\\\":143360,\\\"appver\\\":\\\"2.3.17\\\"}}\",\"MG-Product-Name\":\"music\"}","ctcode":"86","verifyId":1,"e_r":true}`
		target = `/api/sms/captcha/sent-36cd479b6b5-{"deviceId":"4cdb39bf34a848781b89663e1e546b8b","os":"OSX","cellphone":"188********","header":"{\"os\":\"osx\",\"appver\":\"2.3.17\",\"deviceId\":\"7A8EB581-E60B-5230-BB5B-E6DAB1FBFA62%7C5FD718A3-0602-4389-B612-EBEFAA7F108B\",\"requestId\":\"93487028\",\"clientSign\":\"\",\"osver\":\"%E7%89%88%E6%9C%AC12.6%EF%BC%88%E7%89%88%E5%8F%B721G115%EF%BC%89\",\"Nm-GCore-Status\":\"1\",\"MConfig-Info\":\"{\\\"IuRPVVmc3WWul9fT\\\":{\\\"version\\\":143360,\\\"appver\\\":\\\"2.3.17\\\"}}\",\"MG-Product-Name\":\"music\"}","ctcode":"86","verifyId":1,"e_r":true}-36cd479b6b5-6712bc8cd675b8e4059289b0b56abcbe`
	)
	message := fmt.Sprintf("nobody%suse%smd5forencrypt", url, data)
	digest := fmt.Sprintf("%x", md5.Sum([]byte(message)))
	text := fmt.Sprintf("%s-36cd479b6b5-%s-36cd479b6b5-%s", url, data, digest)
	fmt.Printf("text: %s\n", text)
	fmt.Printf("equal: %v\n", text == target)
}

// func Test_digest(t *testing.T) {
// 	var (
// 		sendSMSReq = api.SendSMSReq{
// 			CtCode:    "86",
// 			Cellphone: "188********",
// 			DeviceId:  "4cdb39bf34a848781b89663e1e546b8b",
// 			Os:        "OSX",
// 			VerifyId:  1,
// 			ER:        true,
// 		}
// 		header = api.SendSMSReqHeader{
// 			Os:            "osx",
// 			AppVer:        "2.3.17",
// 			DeviceId:      "7A8EB581-E60B-5230-BB5B-E6DAB1FBFA62%7C5FD718A3-0602-4389-B612-EBEFAA7F108B",
// 			RequestId:     "93487028",
// 			ClientSign:    "",
// 			OsVer:         "%E7%89%88%E6%9C%AC12.6%EF%BC%88%E7%89%88%E5%8F%B721G115%EF%BC%89",
// 			NmGCoreStatus: "1",
// 			MConfigInfo:   `{"IuRPVVmc3WWul9fT":{"version":143360,"appver":"2.3.17"}}`,
// 			MGProductName: "music",
// 		}
// 		url = "/api/sms/captcha/sent"
// 	)
// 	headerByte, err := json.Marshal(header)
// 	if err != nil {
// 		t.Fatalf("json: %s", err)
// 	}
// 	sendSMSReq.Header = string(headerByte)
// 	payload, err := json.Marshal(sendSMSReq)
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	t.Logf("json: %s", string(payload))
// 	t.Logf("digest: %s\n", digest(url, string(payload)))
// }

func TestGetCacheKey(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "sample",
			args: "args123",
			want: "RFKLrid1HPwKv4hPWldxJA==",
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				if err != nil {
					t.Errorf("err: %v args: %s", err, i)
					return false
				}
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CacheKeyEncrypt(tt.args)
			if !tt.wantErr(t, err, fmt.Sprintf("CacheKeyEncrypt(%v)", tt.args)) {
				return
			}
			t.Logf("data: %+v\n", got)
			assert.Equalf(t, tt.want, got, "CacheKeyEncrypt(%v)", tt.args)
		})
	}
}

func TestCacheKeyDecrypt(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "sample",
			args: "RFKLrid1HPwKv4hPWldxJA==",
			want: "args123",
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				if err != nil {
					t.Errorf("err: %v args: %s", err, i)
					return true
				}
				return false
			},
		},
		{
			name: "真实参数",
			args: "0cjs/PeKn8i8GZDV84eJ5IqRq/RX1Hok5Oyt1+2iwcgHfZVdOn+GbulSnnhB4gmf",
			want: "e_r=false&id=10171989900&n=3&s=0",
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				if err != nil {
					t.Errorf("err: %v args: %s", err, i)
					return false
				}
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CacheKeyDecrypt(tt.args)
			if !tt.wantErr(t, err, fmt.Sprintf("CacheKeyDecrypt(%v)", tt.args)) {
				return
			}
			t.Logf("got: %+v\n", got)
			assert.Equalf(t, tt.want, got, "CacheKeyDecrypt(%v)", tt.args)
		})
	}
}

func TestDLLEncodeID(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "sample",
			args: `7F3DB13E-3B90-5859-ACCC-E5BD694285A8%7CB5961B82-C81E-40E9-9164-5BE49896353A`,
			want: "7ciIN1SujXn_nJUbCEIOBA==",
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				if err != nil {
					t.Errorf("err: %v args: %s", err, i)
					return false
				}
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DLLEncodeID(tt.args)
			if !tt.wantErr(t, err, fmt.Sprintf("DLLEncodeID(%v)", tt.args)) {
				return
			}
			assert.Equalf(t, tt.want, got, "DLLEncodeID(%v)", tt.args)
		})
	}
}

func TestAnonymous(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "sample",
			args: "7F3DB13E-3B90-5859-ACCC-E5BD694285A8%7CB5961B82-C81E-40E9-9164-5BE49896353A",
			want: "N0YzREIxM0UtM0I5MC01ODU5LUFDQ0MtRTVCRDY5NDI4NUE4JTdDQjU5NjFCODItQzgxRS00MEU5LTkxNjQtNUJFNDk4OTYzNTNBIDdjaUlOMVN1alhuX25KVWJDRUlPQkE9PQ==",
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				if err != nil {
					t.Errorf("err: %v args: %s", err, i)
					return false
				}
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Anonymous(tt.args)
			if !tt.wantErr(t, err, fmt.Sprintf("Anonymous(%v)", tt.args)) {
				return
			}
			assert.Equalf(t, tt.want, got, "Anonymous(%v)", tt.args)
		})
	}
}

func TestHexDigest(t *testing.T) {
	tests := []struct {
		input  []byte
		expect string
	}{
		{[]byte{0x00}, "93b885adfe0da089cdf634904fd59f71"},
		{[]byte{0xAB, 0xCD}, "7838496fd0586421bbb500bb6f472f13"},
		{[]byte{0x0A, 0x1F}, "22094d34279aa1ba36fa7094cfc90eeb"},
		{[]byte{}, "d41d8cd98f00b204e9800998ecf8427e"},
	}

	for _, tt := range tests {
		got := HexDigest(string(tt.input))
		if got != tt.expect {
			t.Errorf("HexDigest(%x) = %s, want %s", tt.input, got, tt.expect)
		}
	}
}
