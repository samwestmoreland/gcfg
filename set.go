package gcfg

import (
	"bytes"
	"encoding"
	"encoding/gob"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/please-build/gcfg/types"
	"gopkg.in/warnings.v0"
)

type tag struct {
	ident   string
	intMode string
}

func newTag(ts string) tag {
	t := tag{}
	s := strings.Split(ts, ",")
	t.ident = s[0]
	for _, tse := range s[1:] {
		if strings.HasPrefix(tse, "int=") {
			t.intMode = tse[len("int="):]
		}
	}
	return t
}

func fieldFold(v reflect.Value, name string) (reflect.Value, tag) {
	var n string
	r0, _ := utf8.DecodeRuneInString(name)
	if unicode.IsLetter(r0) && !unicode.IsLower(r0) && !unicode.IsUpper(r0) {
		n = "X"
	}
	n += strings.Replace(name, "-", "_", -1)
	f, ok := v.Type().FieldByNameFunc(func(fieldName string) bool {
		if !v.FieldByName(fieldName).CanSet() {
			return false
		}
		f, _ := v.Type().FieldByName(fieldName)
		t := newTag(f.Tag.Get("gcfg"))
		if t.ident != "" {
			return strings.EqualFold(t.ident, name)
		}
		return strings.EqualFold(n, fieldName)
	})
	if !ok {
		return reflect.Value{}, tag{}
	}
	return v.FieldByName(f.Name), newTag(f.Tag.Get("gcfg"))
}

type setter func(destp interface{}, blank bool, val string, t tag) error

var errUnsupportedType = fmt.Errorf("unsupported type")
var errBlankUnsupported = fmt.Errorf("blank value not supported for type")

var setters = []setter{
	typeSetter, textUnmarshalerSetter, kindSetter, scanSetter,
}

func textUnmarshalerSetter(d interface{}, blank bool, val string, t tag) error {
	dtu, ok := d.(encoding.TextUnmarshaler)
	if !ok {
		return errUnsupportedType
	}
	if blank {
		return errBlankUnsupported
	}
	return dtu.UnmarshalText([]byte(val))
}

func boolSetter(d interface{}, blank bool, val string, t tag) error {
	if blank {
		reflect.ValueOf(d).Elem().Set(reflect.ValueOf(true))
		return nil
	}
	b, err := types.ParseBool(val)
	if err == nil {
		reflect.ValueOf(d).Elem().Set(reflect.ValueOf(b))
	}
	return err
}

func intMode(mode string) types.IntMode {
	var m types.IntMode
	if strings.ContainsAny(mode, "dD") {
		m |= types.Dec
	}
	if strings.ContainsAny(mode, "hH") {
		m |= types.Hex
	}
	if strings.ContainsAny(mode, "oO") {
		m |= types.Oct
	}
	return m
}

var typeModes = map[reflect.Type]types.IntMode{
	reflect.TypeOf(int(0)):    types.Dec | types.Hex,
	reflect.TypeOf(int8(0)):   types.Dec | types.Hex,
	reflect.TypeOf(int16(0)):  types.Dec | types.Hex,
	reflect.TypeOf(int32(0)):  types.Dec | types.Hex,
	reflect.TypeOf(int64(0)):  types.Dec | types.Hex,
	reflect.TypeOf(uint(0)):   types.Dec | types.Hex,
	reflect.TypeOf(uint8(0)):  types.Dec | types.Hex,
	reflect.TypeOf(uint16(0)): types.Dec | types.Hex,
	reflect.TypeOf(uint32(0)): types.Dec | types.Hex,
	reflect.TypeOf(uint64(0)): types.Dec | types.Hex,
	// use default mode (allow dec/hex/oct) for uintptr type
	reflect.TypeOf(big.Int{}): types.Dec | types.Hex,
}

func intModeDefault(t reflect.Type) types.IntMode {
	m, ok := typeModes[t]
	if !ok {
		m = types.Dec | types.Hex | types.Oct
	}
	return m
}

func intSetter(d interface{}, blank bool, val string, t tag) error {
	if blank {
		return errBlankUnsupported
	}
	mode := intMode(t.intMode)
	if mode == 0 {
		mode = intModeDefault(reflect.TypeOf(d).Elem())
	}
	return types.ParseInt(d, val, mode)
}

func stringSetter(d interface{}, blank bool, val string, t tag) error {
	if blank {
		return errBlankUnsupported
	}
	dsp, ok := d.(*string)
	if !ok {
		return errUnsupportedType
	}
	*dsp = val
	return nil
}

var kindSetters = map[reflect.Kind]setter{
	reflect.String:  stringSetter,
	reflect.Bool:    boolSetter,
	reflect.Int:     intSetter,
	reflect.Int8:    intSetter,
	reflect.Int16:   intSetter,
	reflect.Int32:   intSetter,
	reflect.Int64:   intSetter,
	reflect.Uint:    intSetter,
	reflect.Uint8:   intSetter,
	reflect.Uint16:  intSetter,
	reflect.Uint32:  intSetter,
	reflect.Uint64:  intSetter,
	reflect.Uintptr: intSetter,
}

var typeSetters = map[reflect.Type]setter{
	reflect.TypeOf(big.Int{}): intSetter,
}

func typeSetter(d interface{}, blank bool, val string, tt tag) error {
	t := reflect.ValueOf(d).Type().Elem()
	setter, ok := typeSetters[t]
	if !ok {
		return errUnsupportedType
	}
	return setter(d, blank, val, tt)
}

func kindSetter(d interface{}, blank bool, val string, tt tag) error {
	k := reflect.ValueOf(d).Type().Elem().Kind()
	setter, ok := kindSetters[k]
	if !ok {
		return errUnsupportedType
	}
	return setter(d, blank, val, tt)
}

func scanSetter(d interface{}, blank bool, val string, tt tag) error {
	if blank {
		return errBlankUnsupported
	}
	return types.ScanFully(d, val, 'v')
}

func newValue(c *warnings.Collector, sect string, vCfg reflect.Value,
	vType reflect.Type) (reflect.Value, error) {
	//
	pv := reflect.New(vType)
	dfltName := "default-" + sect
	dfltField, _ := fieldFold(vCfg, dfltName)
	var err error
	if dfltField.IsValid() {
		b := bytes.NewBuffer(nil)
		ge := gob.NewEncoder(b)
		if err = c.Collect(ge.EncodeValue(dfltField)); err != nil {
			return pv, err
		}
		gd := gob.NewDecoder(bytes.NewReader(b.Bytes()))
		if err = c.Collect(gd.DecodeValue(pv.Elem())); err != nil {
			return pv, err
		}
	}
	return pv, nil
}

func set(c *warnings.Collector, cfg interface{}, sect, sub, name string,
	blank bool, value string, subsectPass bool) error {
	//
	vPCfg := reflect.ValueOf(cfg)
	if vPCfg.Kind() != reflect.Ptr || vPCfg.Elem().Kind() != reflect.Struct {
		panic(fmt.Errorf("config must be a pointer to a struct"))
	}
	vCfg := vPCfg.Elem()
	vSect, _ := fieldFold(vCfg, sect)
	l := loc{section: sect}
	if !vSect.IsValid() {
		err := extraData{loc: l}
		return c.Collect(err)
	}
	isSubsect := vSect.Kind() == reflect.Map
	if subsectPass != isSubsect {
		return nil
	}
	if isSubsect {
		l.subsection = &sub
		vst := vSect.Type()
		if vst.Key().Kind() == reflect.String && vst.Elem().Kind() == reflect.String {
			if vSect.IsNil() {
				vSect.Set(reflect.MakeMap(vst))
			}
			if value != "" {
				if sub != "" {
					vSect.SetMapIndex(reflect.ValueOf(sub+" "+name), reflect.ValueOf(value))
				} else {
					vSect.SetMapIndex(reflect.ValueOf(name), reflect.ValueOf(value))
				}
			}
			return nil
		}
		if vst.Key().Kind() != reflect.String ||
			vst.Elem().Kind() != reflect.Ptr ||
			vst.Elem().Elem().Kind() != reflect.Struct {
			panic(fmt.Errorf("map field for section must have string keys and "+
				" pointer-to-struct or string values: section %q", sect))
		}
		if vSect.IsNil() {
			vSect.Set(reflect.MakeMap(vst))
		}
		k := reflect.ValueOf(sub)
		pv := vSect.MapIndex(k)
		if !pv.IsValid() {
			vType := vSect.Type().Elem().Elem()
			var err error
			if pv, err = newValue(c, sect, vCfg, vType); err != nil {
				return err
			}
			vSect.SetMapIndex(k, pv)
		}
		vSect = pv.Elem()
	} else if vSect.Kind() != reflect.Struct {
		panic(fmt.Errorf("field for section must be a map or a struct: "+
			"section %q", sect))
	} else if sub != "" {
		return c.Collect(extraData{loc: l})
	}
	// Empty name is a special value, meaning that only the
	// section/subsection object is to be created, with no values set.
	if name == "" {
		return nil
	}
	vVar, t := fieldFold(vSect, name)
	l.variable = &name
	if !vVar.IsValid() {
		if ok, err := setExtraDataInSection(vSect, name, value, l); ok {
			return c.Collect(err)
		}
		return nil
	}
	// vVal is either single-valued var, or newly allocated value within multi-valued var
	var vVal reflect.Value
	isMulti := isMultiVal(vVar)
	if isMulti && vVar.Kind() == reflect.Ptr {
		if vVar.IsNil() {
			vVar.Set(reflect.New(vVar.Type().Elem()))
		}
		vVar = vVar.Elem()
	}
	if isMulti && blank {
		vVar.Set(reflect.Zero(vVar.Type()))
		return nil
	}
	if isMulti {
		vVal = reflect.New(vVar.Type().Elem()).Elem()
	} else {
		vVal = vVar
	}
	isDeref := vVal.Type().Name() == "" && vVal.Type().Kind() == reflect.Ptr
	isNew := isDeref && vVal.IsNil()
	// vAddr is address of value to set (dereferenced & allocated as needed)
	var vAddr reflect.Value
	switch {
	case isNew:
		vAddr = reflect.New(vVal.Type().Elem())
	case isDeref && !isNew:
		vAddr = vVal
	default:
		vAddr = vVal.Addr()
	}
	vAddrI := vAddr.Interface()
	err, ok := error(nil), false
	for _, s := range setters {
		err = s(vAddrI, blank, value, t)
		if err == nil {
			ok = true
			break
		}
		if err != errUnsupportedType {
			return locErr{msg: err.Error(), loc: l}
		}
	}
	if !ok {
		// in case all setters returned errUnsupportedType
		return locErr{msg: err.Error(), loc: l}
	}
	if isNew { // set reference if it was dereferenced and newly allocated
		vVal.Set(vAddr)
	}
	if isMulti { // append if multi-valued
		vVar.Set(reflect.Append(vVar, vVal))
	}
	return nil
}

// It is a multi-value if unnamed slice type
func isMultiVal(v reflect.Value) bool {
	return v.Type().Name() == "" && v.Kind() == reflect.Slice ||
		v.Type().Name() == "" && v.Kind() == reflect.Ptr && v.Type().Elem().Name() == "" && v.Type().Elem().Kind() == reflect.Slice
}

func setExtraDataInSection(vSect reflect.Value, key, value string, loc loc) (bool, error) {
	if extraDataField := findExtraDataField(vSect); extraDataField == nil {
		return true, extraData{loc: loc}
	} else if extraDataField.Type() == reflect.TypeOf(map[string]string{}) {
		if extraDataField.IsNil() {
			extraDataField.Set(reflect.ValueOf(map[string]string{key: value}))
			return false, extraData{}
		}
		extraDataField.SetMapIndex(reflect.ValueOf(key), reflect.ValueOf(value))
		return false, extraData{}
	} else if extraDataField.Type() == reflect.TypeOf(map[string][]string{}) {
		if extraDataField.IsNil() {
			extraDataField.Set(reflect.ValueOf(map[string][]string{key: {value}}))
			return false, extraData{}
		}
		if v := extraDataField.MapIndex(reflect.ValueOf(key)); v.IsValid() {
			vs := append(v.Interface().([]string), value)
			extraDataField.SetMapIndex(reflect.ValueOf(key), reflect.ValueOf(vs))
			return false, extraData{}
		}
		extraDataField.SetMapIndex(reflect.ValueOf(key), reflect.ValueOf([]string{value}))
		return false, extraData{}
	} else {
		return true, fmt.Errorf("extra data field must be of type map[string]string or map[string][]string, was %v", extraDataField.Type())
	}
}

func findExtraDataField(vSect reflect.Value) *reflect.Value {
	var value reflect.Value
	for i := 0; i < vSect.NumField(); i++ {
		f := vSect.Type().Field(i)
		if f.Tag.Get("gcfg") == "extra_values" {
			value = vSect.Field(i)
		}
	}
	if !value.IsValid() {
		return nil
	}

	return &value
}
