# Review guide for C# developers

This project is intentionally written so a C# developer can follow it without deep Go experience.

## Mental model

| Go concept | Rough C# analogy |
|---|---|
| `package main` | Console application project |
| `func main()` | `static int Main(string[] args)` / program entry point |
| `type Config struct` | POCO options/settings class |
| `type Totals struct` | DTO for counters |
| `type App struct` | Service class that owns workflow state |
| `func (a *App) run()` | Instance method on `App` |
| `error` return value | Explicit exception-like failure result |
| `defer writer.Close()` | `using` / `finally` cleanup |
| `map[string]bool` | `Dictionary<string,bool>` / `HashSet<string>` substitute |

## Why methods return `error`

Go usually returns errors explicitly instead of throwing exceptions. For example:

```go
if err := a.cleanupFullBackups(dbDir); err != nil {
    return err
}
```

Read this like:

```csharp
try
{
    CleanupFullBackups(dbDir);
}
catch (Exception ex)
{
    throw;
}
```

The difference is that the failure path is visible in the method signature and at every call site.

## Where to start reading

Read in this order:

1. `main()`
2. `loadConfigAndParseArgs()`
3. `App.run()`
4. `findAndProcessBackups()`
5. `cleanupFullBackups()`
6. `cleanupDiffBackups()`
7. `cleanupLogBackups()`
8. `deleteFile()`

## Destructive action boundary

The only function that actually deletes backup files is:

```go
deleteFile(file, reason)
```

That is intentional. Most of the program decides whether files should be kept or deleted; the destructive operation is isolated in one place.

## Why Bash is still invoked

The Go binary invokes Bash only to load the existing shell-compatible config file. This is done to preserve compatibility with the previous script's `source` behavior.

The cleanup logic itself does not shell out to `find`, `date`, `stat`, `sort`, `logger`, or `rm`.

The only operational external command still used is optional `send-pulse`, matching the original telemetry behavior.
