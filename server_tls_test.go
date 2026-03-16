package modbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"testing"
	"time"
)

const (
	clientCertWithRoleOID string = `
-----BEGIN CERTIFICATE-----
MIIFcDCCA1igAwIBAgIUBMfWYE7MKoj0SRxdW0VCC/2ytTUwDQYJKoZIhvcNAQEL
BQAwJjEkMCIGA1UEAwwbVEVTVCBDTElFTlQgQ0VSVCBETyBOT1QgVVNFMB4XDTI1
MTEwNjE0NTUwOFoXDTI2MTEwMTE0NTUwOFowJjEkMCIGA1UEAwwbVEVTVCBDTElF
TlQgQ0VSVCBETyBOT1QgVVNFMIICIjANBgkqhkiG9w0BAQEFAAOCAg8AMIICCgKC
AgEAwHpU/+CQatjwD3IC6jkElH3AnAo8/060jVcF4G51wWl/k/TOe9zS6JUMPF3j
rvSRoJxbPxcyW64CVhSbCCXwqb8J3E+9qc8Z/iVJfDplFAx+slGUoXQ0WFdggIm5
QxOzHh0l/s9sg6I2RzLU6I66DLXu3hZ4ubFnjTQmw6FHlKa5aoAOjTOcJI9r+Sqi
OHjuThIvE8eLvAmFS9fWuBawhAva9CKEU2pETXBMYB5+TFv8Ie8qH/QLqSSK3IEc
553v52hGFkMKMsrfwkkgSNqNErD7FsoQtoFVUVNWwy2VsRPp1mv5xrEJfI6k+Zoq
CRRJTQcKVSF4k8dOT8w8l9u3Se3Np1srpunVUE1Wc126CHaq4EOVQ4eAVV1HOSVc
Ie+3O8dSfcOMM85qrb2UEYrfZb6ntlFs0M9NYhErAcWMwC+NLzaMKpTfRGgURMvt
Si839GFC1rXZMf3Tk9G28p7+vjChEeNHXpIiXYkUn/OJND8tzD6XoaXUEWj8J7h2
uFgSODVOOQbo7vpub/82egvqEHLbBdY/NvtXQH2Aw/uvXsPOjiW5IQhWEtEwQTK5
inSIGp8rNJ6hVF8tnwX9WQo88uGypA78TFhoV4yFYbPN3D6TE2WsLR9SwFuCeS7R
a4/SY1sRBQyEWo1KFTR1o7Ur5bZwMIPyhIHsiN0XcxlnThkCAwEAAaOBlTCBkjAd
BgNVHQ4EFgQUAKD6+VUAPW+yvakv5SdA8KhBFDMwHwYDVR0jBBgwFoAUAKD6+VUA
PW+yvakv5SdA8KhBFDMwDwYDVR0TAQH/BAUwAwEB/zALBgNVHQ8EBAMCAqQwFgYD
VR0lAQH/BAwwCgYIKwYBBQUHAwIwGgYLKwYBBAGDiQyGIgEECwwJb3BlcmF0b3Iy
MA0GCSqGSIb3DQEBCwUAA4ICAQAyl+FHi/TPd32NzCsWLY5NWrY6Mnmc3PN/fg9N
TRlwvzg9I0cZZo2cokm2JlpG7ptg09Rbr0AIPCPqPhX5hj//e/QcWUNwzJb9sP02
xz7U4qy/v4HzHO5NKFb6eqUmsDL03keQ2oKeAye5hQjtxqDJ6SeFlI4nMTl1ca51
pDXTSG/QbAhMcmqyZwN4gmIM0AGgKHkwY8lpyo3DOlR70PGOjrO1bwM99NQupzLd
HI3hvPjvrvUPg5cnlXE069C3o4CU7TCM6Esdq3cijg4DaDf7J2Nhu5ogDGha641O
E6HhyNeTyAURtaswKfhlsSol39b/IK8dadnD1bFG0egKYJf1McW0c9tV/l1u6SNQ
3cLNBs2z2OJ20p5Xn3aZqrcVhcXNWJCQ2OnsSSC83uC+Cbv7lNyjqR3g1CZDe+6a
zRQ1/S0NRGhv93+V2osV4TCQAJQkaYr/AS0M6bZfZvOzj9KkgEg1Fc2YkucnAEjS
A5FFMUKpIMl6dvnlR5fppiqCwWZpav8TzfkGOR8wLBEzyj7ynQYyHxGsMXhrdQCt
Ral9ZHInehNYSdo7+Y59gom0yuAYivqvxROsEnwO5Y2hE1FBM+mOQ5n3J3KZqOEz
x5SNs9khC5yfjS72wokq/l0M2bGdFzogxShoVFjCffRXAmMWg0gUv1DAG9yZwD03
LQlPeg==
-----END CERTIFICATE-----
`

	clientKeyWithRoleOID string = `
-----BEGIN PRIVATE KEY-----
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
IEiAeSZ1uoXSPNCU8EZsAfCagTUQ
-----END PRIVATE KEY-----
`
)

// TestTLSServer tests the TLS layer of the modbus server.
func TestTLSServer(t *testing.T) {
	var err error
	var server *Server
	var serverKeyPair tls.Certificate
	var client1KeyPair tls.Certificate
	var client2KeyPair tls.Certificate
	var clientCp *x509.CertPool
	var serverCp *x509.CertPool
	var th *tlsTestHandler
	var c1 *Client
	var c2 *Client
	var regs []uint16
	var coils []bool

	th = &tlsTestHandler{}

	// load server keypair (from client_tls_test.go)
	serverKeyPair, err = tls.X509KeyPair([]byte(serverCert), []byte(serverKey))
	if err != nil {
		t.Errorf("failed to load test server key pair: %v", err)
		return
	}

	// load the first client keypair (from client_tls_test.go)
	// this client cert doesn't have any Modbus Role extension
	client1KeyPair, err = tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
	if err != nil {
		t.Errorf("failed to load test client key pair: %v", err)
		return
	}

	// load the second client keypair (defined above)
	// this client cert has an "operator2" Modbus Role extension
	client2KeyPair, err = tls.X509KeyPair(
		[]byte(clientCertWithRoleOID), []byte(clientKeyWithRoleOID))
	if err != nil {
		t.Errorf("failed to load test client key pair: %v", err)
		return
	}

	// load the server cert into the client CA cert pool to get the server cert
	// accepted by clients
	clientCp = x509.NewCertPool()
	if !clientCp.AppendCertsFromPEM([]byte(serverCert)) {
		t.Errorf("failed to load test server cert into cert pool")
	}

	// start with an empty server cert pool initially to reject the client
	// certificate
	serverCp = x509.NewCertPool()

	server, err = NewServer(&ServerConfig{
		URL:           "tcp+tls://localhost:5802",
		MaxClients:    2,
		TLSServerCert: &serverKeyPair,
		TLSClientCAs:  serverCp,
	}, th)
	if err != nil {
		t.Errorf("failed to create server: %v", err)
	}

	err = server.Start()
	if err != nil {
		t.Errorf("failed to start server: %v", err)
	}

	// create 2 modbus clients
	c1, err = New(Config{
		URL:           "tcp+tls://localhost:5802",
		TLSClientCert: &client1KeyPair,
		TLSRootCAs:    clientCp,
	})
	if err != nil {
		t.Errorf("failed to create client: %v", err)
	}
	c2, err = New(Config{
		URL:           "tcp+tls://localhost:5802",
		TLSClientCert: &client2KeyPair,
		TLSRootCAs:    clientCp,
	})
	if err != nil {
		t.Errorf("failed to create client: %v", err)
	}

	// attempt to connect and use the first client. since its cert
	// is not trusted by the server, a TLS error should occur on the first
	// request.
	err = c1.Open()
	if err != nil {
		t.Errorf("c1.Open() should have succeeded")
	}
	_, err = c1.ReadCoils(context.Background(), 1, 0, 5)
	if err == nil {
		t.Error("c1.ReadCoils(context.Background(), 1, ) should have failed")
	}
	_ = c1.Close()

	// now place both client certs in the server's authorized client list
	// to get them past the TLS client cert validation procedure
	if !serverCp.AppendCertsFromPEM([]byte(clientCert)) {
		t.Errorf("failed to load client#1 cert into cert pool")
	}
	if !serverCp.AppendCertsFromPEM([]byte(clientCertWithRoleOID)) {
		t.Errorf("failed to load client#2 cert into cert pool")
	}

	// connect both clients: should succeed
	err = c1.Open()
	if err != nil {
		t.Error("c1.Open() should have succeeded")
	}

	err = c2.Open()
	if err != nil {
		t.Error("c2.Open() should have succeeded")
	}

	// client #2 (with 'operator2' role) should have read/write access to coils while
	// client #1 (without role) should only be able to read.
	err = c1.WriteCoil(context.Background(), 1, 0, true)
	if !errors.Is(err, ErrIllegalFunction) {
		t.Errorf("c1.WriteCoil() should have failed with %v, got: %v",
			ErrIllegalFunction, err)
	}

	coils, err = c1.ReadCoils(context.Background(), 1, 0, 5)
	if err != nil {
		t.Errorf("c1.ReadCoils() should have succeeded, got: %v", err)
	}
	if coils[0] {
		t.Errorf("coils[0] should have been false")
	}

	err = c2.WriteCoil(context.Background(), 1, 0, true)
	if err != nil {
		t.Errorf("c2.WriteCoil() should have succeeded, got: %v", err)
	}

	coils, err = c2.ReadCoils(context.Background(), 1, 0, 5)
	if err != nil {
		t.Errorf("c2.ReadCoils() should have succeeded, got: %v", err)
	}
	if !coils[0] {
		t.Errorf("coils[0] should have been true")
	}

	coils, err = c1.ReadCoils(context.Background(), 1, 0, 5)
	if err != nil {
		t.Errorf("c1.ReadCoils() should have succeeded, got: %v", err)
	}
	if !coils[0] {
		t.Errorf("coils[0] should have been true")
	}

	// client #1 should only be allowed access to holding registers of unit id #1
	// while client#2 should be allowed access to holding registers of unit ids #1 and #4
	err = c1.WriteRegister(context.Background(), 1, 2, 100)
	if err != nil {
		t.Errorf("c1.WriteRegister() should have succeeded, got: %v", err)
	}

	err = c1.WriteRegister(context.Background(), 4, 2, 200)
	if !errors.Is(err, ErrIllegalFunction) {
		t.Errorf("c1.WriteRegister() should have failed with %v, got: %v",
			ErrIllegalFunction, err)
	}

	regs, err = c2.ReadRegisters(context.Background(), 1, 1, 2, HoldingRegister)
	if err != nil {
		t.Errorf("c2.ReadRegisters() should have succeeded, got: %v", err)
	}
	if regs[0] != 0 || regs[1] != 100 {
		t.Errorf("unexpected register values: %v", regs)
	}

	err = c2.WriteRegister(context.Background(), 4, 2, 200)
	if err != nil {
		t.Errorf("c2.WriteRegister() should have succeeded, got: %v", err)
	}

	regs, err = c2.ReadRegisters(context.Background(), 4, 1, 2, HoldingRegister)
	if err != nil {
		t.Errorf("c2.ReadRegisters() should have succeeded, got: %v", err)
	}
	if regs[0] != 0 || regs[1] != 200 {
		t.Errorf("unexpected register values: %v", regs)
	}

	// close the server and all client connections
	_ = server.Stop()

	// make sure all underlying TCP client connections have been freed
	time.Sleep(10 * time.Millisecond)
	server.lock.Lock()
	if len(server.tcpClients) != 0 {
		t.Errorf("expected 0 client connections, saw: %v", len(server.tcpClients))
	}
	server.lock.Unlock()

	// cleanup
	_ = c1.Close()
	_ = c2.Close()
}

type tlsTestHandler struct {
	coils      [10]bool
	holdingId1 [10]uint16
	holdingId4 [10]uint16
}

func (th *tlsTestHandler) HandleCoils(ctx context.Context, req *CoilsRequest) (res []bool, err error) {
	// coils access is allowed to any client with a valid cert, but
	// the "operator2" role is required to write
	if req.IsWrite && req.ClientRole != "operator2" {
		err = ErrIllegalFunction
		return
	}

	if req.Addr+req.Quantity > uint16(len(th.coils)) {
		err = ErrIllegalDataAddress
		return
	}

	for i := 0; i < int(req.Quantity); i++ {
		if req.IsWrite {
			th.coils[int(req.Addr)+i] = req.Args[i]
		}
		res = append(res, th.coils[int(req.Addr)+i])
	}

	return
}

func (th *tlsTestHandler) HandleDiscreteInputs(ctx context.Context, req *DiscreteInputsRequest) (res []bool, err error) {
	// there are no digital inputs on this device
	err = ErrIllegalDataAddress

	return
}

func (th *tlsTestHandler) HandleHoldingRegisters(ctx context.Context, req *HoldingRegistersRequest) (res []uint16, err error) {
	// gate unit id #4 behind the "operator2" role while access to unit id #1
	// is allowed to any valid cert
	switch req.UnitID {
	case 0x04:
		if req.ClientRole != "operator2" {
			err = ErrIllegalFunction
			return
		}

		if req.Addr+req.Quantity > uint16(len(th.holdingId4)) {
			err = ErrIllegalDataAddress
			return
		}

		for i := 0; i < int(req.Quantity); i++ {
			if req.IsWrite {
				th.holdingId4[int(req.Addr)+i] = req.Args[i]
			}
			res = append(res, th.holdingId4[int(req.Addr)+i])
		}
	case 0x01:
		if req.Addr+req.Quantity > uint16(len(th.holdingId1)) {
			err = ErrIllegalDataAddress
			return
		}

		for i := 0; i < int(req.Quantity); i++ {
			if req.IsWrite {
				th.holdingId1[int(req.Addr)+i] = req.Args[i]
			}
			res = append(res, th.holdingId1[int(req.Addr)+i])
		}
	default:
		err = ErrIllegalFunction
		return
	}

	return
}

func (th *tlsTestHandler) HandleInputRegisters(ctx context.Context, req *InputRegistersRequest) (res []uint16, err error) {
	// there are no inputs registers on this device
	err = ErrIllegalDataAddress

	return
}

func TestServerExtractRole(t *testing.T) {
	var ms *Server
	var pemBlock *pem.Block
	var x509Cert *x509.Certificate
	var err error
	var role string

	ms = &Server{
		logger: newLogger("test-server-role-extraction", nil),
	}

	// load a client cert without role OID
	pemBlock, _ = pem.Decode([]byte(clientCert))
	if pemBlock == nil {
		t.Errorf("failed to decode client cert")
		return
	}

	x509Cert, err = x509.ParseCertificate(pemBlock.Bytes)
	if err != nil {
		t.Errorf("failed to parse client cert: %v", err)
		return
	}

	// calling extractRole on a cert without role extension should return an
	// empty string (see R-23 of the MBAPS spec)
	role = ms.extractRole(x509Cert)
	if role != "" {
		t.Errorf("role should have been empty, got: '%s'", role)
	}

	// load a certificate with a single role extension of "operator2"
	pemBlock, _ = pem.Decode([]byte(clientCertWithRoleOID))
	if pemBlock == nil {
		t.Errorf("failed to decode client cert")
		return
	}

	x509Cert, err = x509.ParseCertificate(pemBlock.Bytes)
	if err != nil {
		t.Errorf("failed to parse client cert: %v", err)
		return
	}

	role = ms.extractRole(x509Cert)
	if role != "operator2" {
		t.Errorf("role should have been 'operator2', got: '%s'", role)
	}

	// build a certificate with multiple Modbus Role extensions: they should
	// all be rejected
	x509Cert = &x509.Certificate{
		Extensions: []pkix.Extension{
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x0c, 0x04, 0x66, 0x77, 0x67, 0x78,
					// ^ ASN1:UTF8String
					//     ^ length
					//          ^ 4-byte string 'fwgx'
				},
			},
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x0c, 0x02, 0x66, 0x67,
					// ^ ASN1:UTF8String
					//     ^ length
					//          ^ 2-byte string 'fwwf'
				},
			},
		},
	}

	role = ms.extractRole(x509Cert)
	if role != "" {
		t.Errorf("role should have been empty, got: '%s'", role)
	}

	// build a certificate with a single Modbus Role extension of the wrong
	// type: the role should be rejected
	x509Cert = &x509.Certificate{
		Extensions: []pkix.Extension{
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x13, 0x04, 0x66, 0x77, 0x67, 0x78,
					// ^ ASN1:PrintableString
					//     ^ length
					//          ^ 4-byte string 'fwgx'
				},
			},
		},
	}

	role = ms.extractRole(x509Cert)
	if role != "" {
		t.Errorf("role should have been empty, got: '%s'", role)
	}

	// build a certificate with a single, short Modbus Role extension: the role
	// should be rejected
	x509Cert = &x509.Certificate{
		Extensions: []pkix.Extension{
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x0c,
					// ^ ASN1:UTF8String
					//    ^ missing length + payload bytes
				},
			},
		},
	}

	role = ms.extractRole(x509Cert)
	if role != "" {
		t.Errorf("role should have been empty, got: '%s'", role)
	}

	// build a certificate with one bad Modbus Role extension (short) and one
	// valid: they should both be rejected
	x509Cert = &x509.Certificate{
		Extensions: []pkix.Extension{
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x0c,
					// ^ ASN1:UTF8String
					//    ^ missing length + payload bytes
				},
			},
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x0c, 0x02, 0x66, 0x67,
					// ^ ASN1:UTF8String
					//     ^ length
					//          ^ 2-byte string 'fwwf'
				},
			},
		},
	}

	role = ms.extractRole(x509Cert)
	if role != "" {
		t.Errorf("role should have been empty, got: '%s'", role)
	}

	// build a certificate with a single, valid Modbus Role extension: it should be
	// accepted
	x509Cert = &x509.Certificate{
		Extensions: []pkix.Extension{
			{
				Id: modbusRoleOID,
				Value: []byte{
					0x0c, 0x04, 0x66, 0x77, 0x67, 0x78,
					// ^ ASN1:UTF8String
					//     ^ length
					//          ^ 4-byte string 'fwgx'
				},
			},
		},
	}

	role = ms.extractRole(x509Cert)
	if role != "fwgx" {
		t.Errorf("role should have been 'fwgx', got: '%s'", role)
	}
}
