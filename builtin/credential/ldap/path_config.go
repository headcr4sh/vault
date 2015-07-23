package ldap

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/go-ldap/ldap"
	"github.com/hashicorp/vault/logical"
	"github.com/hashicorp/vault/logical/framework"
)

func pathConfig(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: `config`,
		Fields: map[string]*framework.FieldSchema{
			"url": &framework.FieldSchema{
				Type:        framework.TypeString,
				Description: "ldap URL to connect to (default: ldap://127.0.0.1)",
			},
			"userdn": &framework.FieldSchema{
				Type:        framework.TypeString,
				Description: "LDAP domain to use for users (eg: ou=People,dc=example,dc=org)",
			},
			"groupdn": &framework.FieldSchema{
				Type:        framework.TypeString,
				Description: "LDAP domain to use for groups (eg: ou=Groups,dc=example,dc=org)",
			},
			"upndomain": &framework.FieldSchema{
				Type:        framework.TypeString,
				Description: "Enables userPrincipalDomain login with [username]@UPNDomain (optional)",
			},
			"userattr": &framework.FieldSchema{
				Type:        framework.TypeString,
				Description: "Attribute used for users (default: cn)",
			},
			"certificate": &framework.FieldSchema{
				Type:        framework.TypeString,
				Description: "CA certificate to use when verifying LDAP server certificate, must be x509 PEM encoded (optional)",
			},
			"insecure_tls": &framework.FieldSchema{
				Type:        framework.TypeBool,
				Description: "Skip LDAP server SSL Certificate verification - VERY insecure (optional)",
			},
			"starttls": &framework.FieldSchema{
				Type:        framework.TypeBool,
				Description: "Issue a StartTLS command after establishing unencrypted connection (optional)",
			},
		},

		Callbacks: map[logical.Operation]framework.OperationFunc{
			logical.ReadOperation:  b.pathConfigRead,
			logical.WriteOperation: b.pathConfigWrite,
		},

		HelpSynopsis:    pathConfigHelpSyn,
		HelpDescription: pathConfigHelpDesc,
	}
}

func (b *backend) Config(req *logical.Request) (*ConfigEntry, error) {
	entry, err := req.Storage.Get("config")
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	var result ConfigEntry
	result.SetDefaults()
	if err := entry.DecodeJSON(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (b *backend) pathConfigRead(
	req *logical.Request, d *framework.FieldData) (*logical.Response, error) {

	cfg, err := b.Config(req)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}

	return &logical.Response{
		Data: map[string]interface{}{
			"url":          cfg.Url,
			"userdn":       cfg.UserDN,
			"groupdn":      cfg.GroupDN,
			"upndomain":    cfg.UPNDomain,
			"userattr":     cfg.UserAttr,
			"certificate":  cfg.Certificate,
			"insecure_tls": cfg.InsecureTLS,
			"starttls":     cfg.StartTLS,
		},
	}, nil
}

func (b *backend) pathConfigWrite(
	req *logical.Request, d *framework.FieldData) (*logical.Response, error) {

	cfg := &ConfigEntry{}
	url := d.Get("url").(string)
	if url != "" {
		cfg.Url = strings.ToLower(url)
	}
	userattr := d.Get("userattr").(string)
	if userattr != "" {
		cfg.UserAttr = strings.ToLower(userattr)
	}
	userdn := d.Get("userdn").(string)
	if userdn != "" {
		cfg.UserDN = userdn
	}
	groupdn := d.Get("groupdn").(string)
	if groupdn != "" {
		cfg.GroupDN = groupdn
	}
	upndomain := d.Get("upndomain").(string)
	if groupdn != "" {
		cfg.UPNDomain = upndomain
	}
	certificate := d.Get("certificate").(string)
	if certificate != "" {
		cfg.Certificate = certificate
	}
	insecureTLS := d.Get("insecure_tls").(bool)
	if insecureTLS {
		cfg.InsecureTLS = insecureTLS
	}
	startTLS := d.Get("starttls").(bool)
	if startTLS {
		cfg.StartTLS = startTLS
	}

	// Try to connect to the LDAP server, to validate the URL configuration
	// We can also check the URL at this stage, as anything else would probably
	// require authentication.
	conn, cerr := cfg.DialLDAP()
	if cerr != nil {
		return logical.ErrorResponse(cerr.Error()), nil
	}
	conn.Close()

	entry, err := logical.StorageEntryJSON("config", cfg)
	if err != nil {
		return nil, err
	}
	if err := req.Storage.Put(entry); err != nil {
		return nil, err
	}

	return nil, nil
}

type ConfigEntry struct {
	Url         string
	UserDN      string
	GroupDN     string
	UPNDomain   string
	UserAttr    string
	Certificate string
	InsecureTLS bool
	StartTLS    bool
}

func (c *ConfigEntry) GetTLSConfig(host string) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		ServerName: host,
	}
	if c.InsecureTLS {
		tlsConfig.InsecureSkipVerify = true
	}
	if c.Certificate != "" {
		caPool := x509.NewCertPool()
		ok := caPool.AppendCertsFromPEM([]byte(c.Certificate))
		if !ok {
			return nil, fmt.Errorf("could not append CA certificate")
		}
		tlsConfig.RootCAs = caPool
	}
	return tlsConfig, nil
}

func (c *ConfigEntry) DialLDAP() (*ldap.Conn, error) {

	u, err := url.Parse(c.Url)
	if err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Host
	}

	var conn *ldap.Conn
	switch u.Scheme {
	case "ldap":
		if port == "" {
			port = "389"
		}
		conn, err = ldap.Dial("tcp", host+":"+port)
		if c.StartTLS {
			tlsConfig, err := c.GetTLSConfig(host)
			if err != nil {
				break
			}
			err = conn.StartTLS(tlsConfig)
		}
	case "ldaps":
		if port == "" {
			port = "636"
		}
		tlsConfig, err := c.GetTLSConfig(host)
		if err != nil {
			break
		}
		conn, err = ldap.DialTLS("tcp", host+":"+port, tlsConfig)
	default:
		return nil, fmt.Errorf("invalid LDAP scheme")
	}
	if err != nil {
		return nil, fmt.Errorf("cannot connect to LDAP: %v", err)
	}

	return conn, nil
}

func (c *ConfigEntry) SetDefaults() {
	c.Url = "ldap://127.0.0.1"
	c.UserAttr = "cn"
}

const pathConfigHelpSyn = `
Configure the LDAP server to connect to.
`

const pathConfigHelpDesc = `
This endpoint allows you to configure the LDAP server to connect to, and give
basic information of the schema of that server.

The LDAP URL can use either the "ldap://" or "ldaps://" schema. In the former
case, an unencrypted connection will be done, with default port 389; in the latter
case, a SSL connection will be done, with default port 636.
`
