//go:build linux

package main

// B679: the D-Bus side of the Secret Service store (keychain_secretservice.go)
// through github.com/godbus/dbus/v5 (pure Go, vendored): the session bus,
// org.freedesktop.secrets at /org/freedesktop/secrets. The session uses the
// "plain" algorithm (the secret travels over the private session bus, as
// the spec allows); dbus-secret-service connects with the DH algorithm, which
// changes only the transport, never the stored item.

import (
	"errors"

	"github.com/godbus/dbus/v5"
)

const (
	ssDest       = "org.freedesktop.secrets"
	ssPath       = dbus.ObjectPath("/org/freedesktop/secrets")
	ssIface      = "org.freedesktop.Secret.Service"
	ssItemIface  = "org.freedesktop.Secret.Item"
	ssCollIface  = "org.freedesktop.Secret.Collection"
	ssPromptIfc  = "org.freedesktop.Secret.Prompt"
	ssNoPrompt   = dbus.ObjectPath("/")
	ssIsLocked   = "org.freedesktop.Secret.Error.IsLocked"
	ssNoSuchItem = "org.freedesktop.Secret.Error.NoSuchObject"
)

// ssSecret is the spec's Secret struct (oayays).
type ssSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

type ssDBus struct {
	conn    *dbus.Conn
	session dbus.ObjectPath
}

// ssDial is SecretService::connect: the session bus and an open session.
func ssDial() (ssAPI, error) {
	conn, err := dbus.SessionBusPrivate()
	if err != nil {
		return nil, err
	}
	if err = conn.Auth(nil); err == nil {
		err = conn.Hello()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	var output dbus.Variant
	var session dbus.ObjectPath
	call := conn.Object(ssDest, ssPath).Call(ssIface+".OpenSession", 0, "plain", dbus.MakeVariant(""))
	if err := call.Store(&output, &session); err != nil {
		conn.Close()
		return nil, err
	}
	return &ssDBus{conn: conn, session: session}, nil
}

func ssMapErr(err error) error {
	var dErr dbus.Error
	if errors.As(err, &dErr) && (dErr.Name == ssIsLocked || dErr.Name == ssNoSuchItem) {
		return errors.Join(ssLocked, err)
	}
	return err
}

func ssPaths(in []dbus.ObjectPath) []string {
	out := make([]string, len(in))
	for i, p := range in {
		out[i] = string(p)
	}
	return out
}

func (d *ssDBus) Search(attrs map[string]string) ([]string, []string, error) {
	var unlocked, locked []dbus.ObjectPath
	err := d.conn.Object(ssDest, ssPath).Call(ssIface+".SearchItems", 0, attrs).Store(&unlocked, &locked)
	if err != nil {
		return nil, nil, ssMapErr(err)
	}
	return ssPaths(unlocked), ssPaths(locked), nil
}

// prompt runs a prompt the service returned and waits for Completed; a
// dismissed prompt is Error::Prompt (NoStorageAccess).
func (d *ssDBus) prompt(path dbus.ObjectPath) error {
	if path == ssNoPrompt || path == "" {
		return nil
	}
	opts := []dbus.MatchOption{dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(ssPromptIfc), dbus.WithMatchMember("Completed")}
	if err := d.conn.AddMatchSignal(opts...); err != nil {
		return err
	}
	defer d.conn.RemoveMatchSignal(opts...)
	signals := make(chan *dbus.Signal, 8)
	d.conn.Signal(signals)
	defer d.conn.RemoveSignal(signals)
	if err := d.conn.Object(ssDest, path).Call(ssPromptIfc+".Prompt", 0, "").Err; err != nil {
		return ssMapErr(err)
	}
	for signal := range signals {
		if signal.Path != path || signal.Name != ssPromptIfc+".Completed" {
			continue
		}
		if len(signal.Body) > 0 {
			if dismissed, ok := signal.Body[0].(bool); ok && dismissed {
				return ssLocked
			}
		}
		return nil
	}
	return errors.New("D-Bus connection closed while waiting for a prompt")
}

func (d *ssDBus) Unlock(items []string) error {
	objects := make([]dbus.ObjectPath, len(items))
	for i, item := range items {
		objects[i] = dbus.ObjectPath(item)
	}
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := d.conn.Object(ssDest, ssPath).Call(ssIface+".Unlock", 0, objects).Store(&unlocked, &prompt); err != nil {
		return ssMapErr(err)
	}
	return d.prompt(prompt)
}

func (d *ssDBus) Secret(item string) ([]byte, error) {
	var secret ssSecret
	if err := d.conn.Object(ssDest, dbus.ObjectPath(item)).Call(ssItemIface+".GetSecret", 0, d.session).Store(&secret); err != nil {
		return nil, ssMapErr(err)
	}
	return secret.Value, nil
}

func (d *ssDBus) SetSecret(item string, value []byte) error {
	secret := ssSecret{Session: d.session, Parameters: []byte{}, Value: value, ContentType: "application/octet-stream"}
	return ssMapErr(d.conn.Object(ssDest, dbus.ObjectPath(item)).Call(ssItemIface+".SetSecret", 0, secret).Err)
}

// Create is service.rs create_item on the "default" collection: the default
// alias, unlocked when locked, CreateItem with replace.
func (d *ssDBus) Create(label string, attrs map[string]string, value []byte) error {
	var coll dbus.ObjectPath
	if err := d.conn.Object(ssDest, ssPath).Call(ssIface+".ReadAlias", 0, "default").Store(&coll); err != nil {
		return ssMapErr(err)
	}
	if coll == ssNoPrompt {
		return errors.Join(ssLocked, errors.New("no default collection"))
	}
	locked, err := d.conn.Object(ssDest, coll).GetProperty(ssCollIface + ".Locked")
	if err != nil {
		return ssMapErr(err)
	}
	if isLocked, _ := locked.Value().(bool); isLocked {
		if err := d.Unlock([]string{string(coll)}); err != nil {
			return err
		}
	}
	props := map[string]dbus.Variant{
		ssItemIface + ".Label":      dbus.MakeVariant(label),
		ssItemIface + ".Attributes": dbus.MakeVariant(attrs),
	}
	secret := ssSecret{Session: d.session, Parameters: []byte{}, Value: value, ContentType: "application/octet-stream"}
	var item, prompt dbus.ObjectPath
	if err := d.conn.Object(ssDest, coll).Call(ssCollIface+".CreateItem", 0, props, secret, true).Store(&item, &prompt); err != nil {
		return ssMapErr(err)
	}
	return d.prompt(prompt)
}

func (d *ssDBus) Delete(item string) error {
	var prompt dbus.ObjectPath
	if err := d.conn.Object(ssDest, dbus.ObjectPath(item)).Call(ssItemIface+".Delete", 0).Store(&prompt); err != nil {
		return ssMapErr(err)
	}
	return d.prompt(prompt)
}
