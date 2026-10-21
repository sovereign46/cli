package pi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

type piModelsProjection struct {
	rootObject       bool
	providersPresent bool
	providersObject  bool
	s46Present       bool
	s46Object        bool
	modelsPresent    bool
	modelsArray      bool
	models           []piModelProjection
}

type piModelProjection struct {
	object    bool
	idPresent bool
	idString  bool
	id        string
}

type piSettingsProjection struct {
	rootObject      bool
	providerPresent bool
	providerString  bool
	provider        string
	modelPresent    bool
	modelString     bool
	model           string
}

func guardedPiModelIndex(content []byte) (int, bool) {
	projection, err := projectPiModels(content)
	if err != nil || !projection.providersObject || !projection.s46Object || !projection.modelsArray {
		return 0, false
	}
	for i, model := range projection.models {
		if model.object && model.idString && model.id == deepSeekModelID {
			return i, true
		}
	}
	return 0, false
}

func guardedPiDefaultSelected(content []byte) bool {
	projection, err := projectPiSettings(content)
	return err == nil && projection.providerString && projection.provider == "s46" && projection.modelString && projection.model == deepSeekModelID
}

func projectPiModels(content []byte) (piModelsProjection, error) {
	parser := piJSONParser{input: stripPiModelJSON(content)}
	first, err := parser.next()
	if err != nil {
		return piModelsProjection{}, err
	}
	if first.kind != piJSONObjectStart {
		if err := parser.skipValueFrom(first); err != nil {
			return piModelsProjection{}, err
		}
		return piModelsProjection{}, parser.finish()
	}
	projection, err := parser.projectModelsRoot()
	projection.rootObject = true
	if err != nil {
		return piModelsProjection{}, err
	}
	return projection, parser.finish()
}

func projectPiSettings(content []byte) (piSettingsProjection, error) {
	input := bytes.ToValidUTF8(stripPiBOM(content), []byte("\xef\xbf\xbd"))
	parser := piJSONParser{input: input}
	first, err := parser.next()
	if err != nil {
		return piSettingsProjection{}, err
	}
	if first.kind != piJSONObjectStart {
		if err := parser.skipValueFrom(first); err != nil {
			return piSettingsProjection{}, err
		}
		return piSettingsProjection{}, parser.finish()
	}
	projection, err := parser.projectSettingsRoot()
	projection.rootObject = true
	if err != nil {
		return piSettingsProjection{}, err
	}
	return projection, parser.finish()
}

func (p *piJSONParser) projectModelsRoot() (piModelsProjection, error) {
	var result piModelsProjection
	err := p.eachObjectMember(func(key string, value piJSONToken) error {
		if key != "providers" {
			return p.skipValueFrom(value)
		}
		result.providersPresent = true
		result.providersObject = value.kind == piJSONObjectStart
		result.s46Present = false
		result.s46Object = false
		result.modelsPresent = false
		result.modelsArray = false
		result.models = nil
		if !result.providersObject {
			return p.skipValueFrom(value)
		}
		return p.projectProviders(&result)
	})
	return result, err
}

func (p *piJSONParser) projectProviders(result *piModelsProjection) error {
	return p.eachObjectMember(func(key string, value piJSONToken) error {
		if key != "s46" {
			return p.skipValueFrom(value)
		}
		result.s46Present = true
		result.s46Object = value.kind == piJSONObjectStart
		result.modelsPresent = false
		result.modelsArray = false
		result.models = nil
		if !result.s46Object {
			return p.skipValueFrom(value)
		}
		return p.projectS46Provider(result)
	})
}

func (p *piJSONParser) projectS46Provider(result *piModelsProjection) error {
	return p.eachObjectMember(func(key string, value piJSONToken) error {
		if key != "models" {
			return p.skipValueFrom(value)
		}
		result.modelsPresent = true
		result.modelsArray = value.kind == piJSONArrayStart
		result.models = nil
		if !result.modelsArray {
			return p.skipValueFrom(value)
		}
		models, err := p.projectModelsArray()
		if err != nil {
			return err
		}
		result.models = models
		return nil
	})
}

func (p *piJSONParser) projectModelsArray() ([]piModelProjection, error) {
	var result []piModelProjection
	err := p.eachArrayValue(func(value piJSONToken) error {
		model := piModelProjection{object: value.kind == piJSONObjectStart}
		if !model.object {
			if err := p.skipValueFrom(value); err != nil {
				return err
			}
			result = append(result, model)
			return nil
		}
		if err := p.eachObjectMember(func(key string, value piJSONToken) error {
			if key != "id" {
				return p.skipValueFrom(value)
			}
			model.idPresent = true
			model.idString = value.kind == piJSONString
			model.id = ""
			if model.idString {
				model.id = value.value
				return nil
			}
			return p.skipValueFrom(value)
		}); err != nil {
			return err
		}
		result = append(result, model)
		return nil
	})
	return result, err
}

func (p *piJSONParser) projectSettingsRoot() (piSettingsProjection, error) {
	var result piSettingsProjection
	err := p.eachObjectMember(func(key string, value piJSONToken) error {
		switch key {
		case "defaultProvider":
			result.providerPresent = true
			result.providerString = value.kind == piJSONString
			result.provider = ""
			if result.providerString {
				result.provider = value.value
				return nil
			}
		case "defaultModel":
			result.modelPresent = true
			result.modelString = value.kind == piJSONString
			result.model = ""
			if result.modelString {
				result.model = value.value
				return nil
			}
		}
		return p.skipValueFrom(value)
	})
	return result, err
}

func (p *piJSONParser) eachObjectMember(visit func(string, piJSONToken) error) error {
	token, err := p.next()
	if err != nil {
		return err
	}
	if token.kind == piJSONObjectEnd {
		return nil
	}
	for {
		if token.kind != piJSONString {
			return fmt.Errorf("JSON object key must be a string")
		}
		key := token.value
		colon, err := p.next()
		if err != nil {
			return err
		}
		if colon.kind != piJSONColon {
			return fmt.Errorf("JSON object key is missing colon")
		}
		value, err := p.next()
		if err != nil {
			return err
		}
		if err := visit(key, value); err != nil {
			return err
		}
		separator, err := p.next()
		if err != nil {
			return err
		}
		if separator.kind == piJSONObjectEnd {
			return nil
		}
		if separator.kind != piJSONComma {
			return fmt.Errorf("JSON object member is missing comma")
		}
		token, err = p.next()
		if err != nil {
			return err
		}
		if token.kind == piJSONObjectEnd {
			return fmt.Errorf("JSON object has trailing comma")
		}
	}
}

func (p *piJSONParser) eachArrayValue(visit func(piJSONToken) error) error {
	token, err := p.next()
	if err != nil {
		return err
	}
	if token.kind == piJSONArrayEnd {
		return nil
	}
	for {
		if err := visit(token); err != nil {
			return err
		}
		separator, err := p.next()
		if err != nil {
			return err
		}
		if separator.kind == piJSONArrayEnd {
			return nil
		}
		if separator.kind != piJSONComma {
			return fmt.Errorf("JSON array element is missing comma")
		}
		token, err = p.next()
		if err != nil {
			return err
		}
		if token.kind == piJSONArrayEnd {
			return fmt.Errorf("JSON array has trailing comma")
		}
	}
}

type piJSONTokenKind uint8

const (
	piJSONEOF piJSONTokenKind = iota
	piJSONObjectStart
	piJSONObjectEnd
	piJSONArrayStart
	piJSONArrayEnd
	piJSONColon
	piJSONComma
	piJSONString
	piJSONScalar
)

type piJSONToken struct {
	kind  piJSONTokenKind
	value string
}

type piJSONParser struct {
	input  []byte
	offset int
}

type piJSONFrame struct {
	object bool
	state  uint8
}

func (p *piJSONParser) finish() error {
	token, err := p.next()
	if err != nil {
		return err
	}
	if token.kind != piJSONEOF {
		return fmt.Errorf("JSON has trailing content")
	}
	return nil
}

func (p *piJSONParser) next() (piJSONToken, error) {
	for p.offset < len(p.input) && isPiJSONWhitespace(p.input[p.offset]) {
		p.offset++
	}
	if p.offset == len(p.input) {
		return piJSONToken{kind: piJSONEOF}, nil
	}
	current := p.input[p.offset]
	p.offset++
	switch current {
	case '{':
		return piJSONToken{kind: piJSONObjectStart}, nil
	case '}':
		return piJSONToken{kind: piJSONObjectEnd}, nil
	case '[':
		return piJSONToken{kind: piJSONArrayStart}, nil
	case ']':
		return piJSONToken{kind: piJSONArrayEnd}, nil
	case ':':
		return piJSONToken{kind: piJSONColon}, nil
	case ',':
		return piJSONToken{kind: piJSONComma}, nil
	case '"':
		return p.stringToken(p.offset - 1)
	case 't':
		return p.literalToken("true")
	case 'f':
		return p.literalToken("false")
	case 'n':
		return p.literalToken("null")
	default:
		if current == '-' || current >= '0' && current <= '9' {
			return p.numberToken(p.offset - 1)
		}
		return piJSONToken{}, fmt.Errorf("invalid JSON token at byte %d", p.offset-1)
	}
}

func (p *piJSONParser) stringToken(start int) (piJSONToken, error) {
	for p.offset < len(p.input) {
		current := p.input[p.offset]
		p.offset++
		switch {
		case current == '"':
			var value string
			if err := json.Unmarshal(p.input[start:p.offset], &value); err != nil {
				return piJSONToken{}, err
			}
			return piJSONToken{kind: piJSONString, value: value}, nil
		case current < 0x20:
			return piJSONToken{}, fmt.Errorf("JSON string contains a control byte")
		case current == '\\':
			if p.offset == len(p.input) {
				return piJSONToken{}, fmt.Errorf("unterminated JSON string escape")
			}
			escape := p.input[p.offset]
			p.offset++
			if escape == 'u' {
				if p.offset+4 > len(p.input) {
					return piJSONToken{}, fmt.Errorf("short JSON unicode escape")
				}
				for _, digit := range p.input[p.offset : p.offset+4] {
					if !isHexDigit(digit) {
						return piJSONToken{}, fmt.Errorf("invalid JSON unicode escape")
					}
				}
				p.offset += 4
			} else if !bytes.ContainsRune([]byte(`"\\/bfnrt`), rune(escape)) {
				return piJSONToken{}, fmt.Errorf("invalid JSON string escape")
			}
		}
	}
	return piJSONToken{}, fmt.Errorf("unterminated JSON string")
}

func (p *piJSONParser) literalToken(literal string) (piJSONToken, error) {
	start := p.offset - 1
	end := start + len(literal)
	if end > len(p.input) || string(p.input[start:end]) != literal {
		return piJSONToken{}, fmt.Errorf("invalid JSON literal")
	}
	p.offset = end
	return piJSONToken{kind: piJSONScalar}, nil
}

func (p *piJSONParser) numberToken(start int) (piJSONToken, error) {
	i := start
	if p.input[i] == '-' {
		i++
		if i == len(p.input) {
			return piJSONToken{}, fmt.Errorf("invalid JSON number")
		}
	}
	if p.input[i] == '0' {
		i++
	} else if p.input[i] >= '1' && p.input[i] <= '9' {
		for i < len(p.input) && p.input[i] >= '0' && p.input[i] <= '9' {
			i++
		}
	} else {
		return piJSONToken{}, fmt.Errorf("invalid JSON number")
	}
	if i < len(p.input) && p.input[i] == '.' {
		i++
		fraction := i
		for i < len(p.input) && p.input[i] >= '0' && p.input[i] <= '9' {
			i++
		}
		if i == fraction {
			return piJSONToken{}, fmt.Errorf("invalid JSON number fraction")
		}
	}
	if i < len(p.input) && (p.input[i] == 'e' || p.input[i] == 'E') {
		i++
		if i < len(p.input) && (p.input[i] == '+' || p.input[i] == '-') {
			i++
		}
		exponent := i
		for i < len(p.input) && p.input[i] >= '0' && p.input[i] <= '9' {
			i++
		}
		if i == exponent {
			return piJSONToken{}, fmt.Errorf("invalid JSON number exponent")
		}
	}
	p.offset = i
	return piJSONToken{kind: piJSONScalar}, nil
}

func (p *piJSONParser) skipValueFrom(first piJSONToken) error {
	if first.kind == piJSONString || first.kind == piJSONScalar {
		return nil
	}
	const (
		objectKeyOrEnd uint8 = iota
		objectColon
		objectValue
		objectCommaOrEnd
		objectKey
		arrayValueOrEnd
		arrayCommaOrEnd
		arrayValue
	)
	var stack []piJSONFrame
	switch first.kind {
	case piJSONObjectStart:
		stack = append(stack, piJSONFrame{object: true, state: objectKeyOrEnd})
	case piJSONArrayStart:
		stack = append(stack, piJSONFrame{state: arrayValueOrEnd})
	default:
		return fmt.Errorf("expected JSON value")
	}
	for len(stack) > 0 {
		token, err := p.next()
		if err != nil {
			return err
		}
		top := &stack[len(stack)-1]
		if top.object {
			switch top.state {
			case objectKeyOrEnd:
				if token.kind == piJSONObjectEnd {
					stack = stack[:len(stack)-1]
					continue
				}
				if token.kind != piJSONString {
					return fmt.Errorf("JSON object key must be a string")
				}
				top.state = objectColon
			case objectKey:
				if token.kind != piJSONString {
					return fmt.Errorf("JSON object key must follow comma")
				}
				top.state = objectColon
			case objectColon:
				if token.kind != piJSONColon {
					return fmt.Errorf("JSON object key is missing colon")
				}
				top.state = objectValue
			case objectValue:
				top.state = objectCommaOrEnd
				if err := pushPiJSONValue(&stack, token, objectKeyOrEnd, arrayValueOrEnd); err != nil {
					return err
				}
			case objectCommaOrEnd:
				switch token.kind {
				case piJSONObjectEnd:
					stack = stack[:len(stack)-1]
				case piJSONComma:
					top.state = objectKey
				default:
					return fmt.Errorf("JSON object member is missing comma")
				}
			}
			continue
		}
		switch top.state {
		case arrayValueOrEnd:
			if token.kind == piJSONArrayEnd {
				stack = stack[:len(stack)-1]
				continue
			}
			top.state = arrayCommaOrEnd
			if err := pushPiJSONValue(&stack, token, objectKeyOrEnd, arrayValueOrEnd); err != nil {
				return err
			}
		case arrayValue:
			top.state = arrayCommaOrEnd
			if err := pushPiJSONValue(&stack, token, objectKeyOrEnd, arrayValueOrEnd); err != nil {
				return err
			}
		case arrayCommaOrEnd:
			switch token.kind {
			case piJSONArrayEnd:
				stack = stack[:len(stack)-1]
			case piJSONComma:
				top.state = arrayValue
			default:
				return fmt.Errorf("JSON array element is missing comma")
			}
		}
	}
	return nil
}

func pushPiJSONValue(stack *[]piJSONFrame, token piJSONToken, objectState, arrayState uint8) error {
	switch token.kind {
	case piJSONString, piJSONScalar:
		return nil
	case piJSONObjectStart:
		*stack = append(*stack, piJSONFrame{object: true, state: objectState})
		return nil
	case piJSONArrayStart:
		*stack = append(*stack, piJSONFrame{state: arrayState})
		return nil
	default:
		return fmt.Errorf("expected JSON value")
	}
}

// stripPiModelJSON mirrors Pi's models.json preprocessing: UTF-8 BOM removal,
// // line-comment removal, and trailing-comma removal outside string literals.
func stripPiModelJSON(content []byte) []byte {
	input := bytes.ToValidUTF8(stripPiBOM(content), []byte("\xef\xbf\xbd"))
	withoutComments := make([]byte, 0, len(input))
	for i, inString, escaped := 0, false, false; i < len(input); i++ {
		current := input[i]
		if inString {
			withoutComments = append(withoutComments, current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			withoutComments = append(withoutComments, current)
			continue
		}
		if current == '/' && i+1 < len(input) && input[i+1] == '/' {
			for i+1 < len(input) && input[i+1] != '\n' {
				i++
			}
			continue
		}
		withoutComments = append(withoutComments, current)
	}

	result := make([]byte, 0, len(withoutComments))
	for i, inString, escaped := 0, false, false; i < len(withoutComments); i++ {
		current := withoutComments[i]
		if inString {
			result = append(result, current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			result = append(result, current)
			continue
		}
		if current == ',' && piTrailingComma(withoutComments[i+1:]) {
			continue
		}
		result = append(result, current)
	}
	return result
}

func stripPiBOM(content []byte) []byte {
	return bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
}

func piTrailingComma(tail []byte) bool {
	for len(tail) > 0 {
		r, size := utf8.DecodeRune(tail)
		if r == '}' || r == ']' {
			return true
		}
		if !piJavaScriptWhitespace(r) {
			return false
		}
		tail = tail[size:]
	}
	return false
}

func piJavaScriptWhitespace(r rune) bool {
	switch {
	case r >= '\t' && r <= '\r', r == ' ', r == '\u00a0', r == '\u1680',
		r >= '\u2000' && r <= '\u200a', r == '\u2028', r == '\u2029',
		r == '\u202f', r == '\u205f', r == '\u3000', r == '\ufeff':
		return true
	default:
		return false
	}
}

func isPiJSONWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func isHexDigit(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}
