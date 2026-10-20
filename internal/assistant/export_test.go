package assistant

var ToolOK = toolOK
var LanguageDirective = languageDirective

// AllowedAfterOutsiderText reports whether a tool may still run in a turn that read third-party text.
func AllowedAfterOutsiderText(tool string) bool { return afterOutsiderText[tool] }
