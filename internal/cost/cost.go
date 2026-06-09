package cost

// usdPerMillionChars holds approximate published TTS prices in USD per 1,000,000
// characters. These are estimates for showing rough spend, not exact billing
// (gpt-4o-mini-tts is token-priced upstream; the value here approximates it).
var usdPerMillionChars = map[string]map[string]float64{
	"openai": {"tts-1": 15.0, "tts-1-hd": 30.0, "gpt-4o-mini-tts": 12.0},
	"grok":   {"grok": 5.0},
}

// defaultModel is the model name assumed when a provider is used without an
// explicit model.
var defaultModel = map[string]string{
	"openai": "tts-1",
	"grok":   "grok",
	"piper":  "piper",
}

// EffectiveModel returns model, or the provider's default when model is empty.
func EffectiveModel(provider, model string) string {
	if model != "" {
		return model
	}
	return defaultModel[provider]
}

// CentsFor estimates the cost in cents of synthesizing chars characters with the
// given provider+model. An empty model uses the provider default. Unknown
// provider/model returns 0.
func CentsFor(provider, model string, chars int) float64 {
	rates, ok := usdPerMillionChars[provider]
	if !ok {
		return 0
	}
	rate, ok := rates[EffectiveModel(provider, model)]
	if !ok {
		return 0
	}
	return float64(chars) * rate / 1_000_000.0 * 100.0
}

// ModelsFor returns the models offered for a provider (stable order), or nil.
func ModelsFor(provider string) []string {
	switch provider {
	case "openai":
		return []string{"tts-1", "tts-1-hd", "gpt-4o-mini-tts"}
	case "grok":
		return []string{"grok"}
	}
	return nil
}
