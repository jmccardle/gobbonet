<#
  GobboNet search proxy — loopback HTTP relay for the chat UI's web search.

  Extracted from launch.bat, where it lived as a base64 -EncodedCommand blob. Same behaviour,
  same port, same defaults — it is just readable now, which is the point: GobboNet's pitch is
  "open the install folder and read it", and one of the two network paths in the product was a
  wall of base64. Nothing else in the tree is encoded like that; fileserver.ps1 is already a
  plain file for the same reason.

  WHAT CHANGED BEYOND THE EXTRACTION: a provider switch, so the search backend is a choice
  rather than a hardcoded vendor.

  Why: the front page says "No account, no sign-up, no email" — and then web search asks the
  user to create an Ollama account and paste an API key. This opens a second door without
  closing the first: set SEARCH_URL and the proxy uses it; set nothing and it relays to
  Ollama exactly as it does today. NO EXISTING INSTALL CHANGES BEHAVIOUR.

  PROVIDERS
    auto        (DEFAULT)  use SEARCH_URL if it is set, else fall back to ollama. Chosen so
                           the default can never be worse than what shipped before.
    http                   forward to SEARCH_URL — a self-hosted SearxNG, or anything else
                           speaking {query,max_results} -> {results:[{title,url,content}]}.
                           Server-side so CORS does not apply: the browser could never call a
                           search engine directly, which is why this proxy exists at all.
                           An unset SEARCH_URL is REPORTED (502 with a reason), never silently
                           empty — "not configured" and "found nothing" must not look alike,
                           which is the failure this whole file is written against.
    ollama                 relay to https://ollama.com/api, forwarding the Authorization
                           header exactly as before. Unchanged, and still what you get when
                           no SEARCH_URL is configured.

  Bound to loopback only, as before: the file server's /search route reaches it via 127.0.0.1,
  so it is not exposed on the LAN and needs no auth of its own.

  Run:  powershell -NoProfile -ExecutionPolicy Bypass -File searchproxy.ps1
  Env:  GEMMA_SEARCH_PORT (default 11435)   SEARCH_PROVIDER (default auto: ollama unless SEARCH_URL is set)
#>

$ErrorActionPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$Port     = if ($env:GEMMA_SEARCH_PORT) { [int]$env:GEMMA_SEARCH_PORT } else { 11435 }
# Default: 'auto'. Use SEARCH_URL if one is configured, otherwise relay to Ollama
# exactly as before.
#
# This deliberately does NOT flip the default to 'http'. An earlier draft did, and
# it was a REGRESSION: SEARCH_URL is unset on a fresh install, so every new user
# would have got a 502 where an Ollama key previously worked. Making search worse
# by default is not a fix for search requiring an account.
#
# So the change is strictly additive. Nothing that works today stops working; a
# user who points SEARCH_URL at a keyless backend gets one, and it is chosen
# automatically rather than needing two variables set.
$Provider = if ($env:SEARCH_PROVIDER) { $env:SEARCH_PROVIDER.ToLower() } else { 'auto' }
if ($Provider -eq 'auto') { $Provider = if ($env:SEARCH_URL) { 'http' } else { 'ollama' } }
# Where 'http' provider sends the search. Any service speaking {query,max_results} ->
# {results:[{title,url,content}]}. No default: an unset URL means the provider is not
# configured, which is reported rather than guessed at.
$SearchUrl = $env:SEARCH_URL

function Write-Json {
    param($Response, [string]$Text, [int]$Status = 200)
    $Response.StatusCode  = $Status
    $Response.ContentType = 'application/json'
    $bytes = [Text.Encoding]::UTF8.GetBytes($Text)
    $Response.OutputStream.Write($bytes, 0, $bytes.Length)
    $Response.Close()
}

function ConvertTo-JsonString {
    # Hand-rolled because Windows PowerShell 5.1's ConvertTo-Json escapes non-ASCII into \uXXXX
    # and mangles some punctuation that turns up constantly in search snippets.
    param([string]$s)
    if ($null -eq $s) { return '""' }
    $sb = [Text.StringBuilder]::new()
    [void]$sb.Append('"')
    foreach ($ch in $s.ToCharArray()) {
        switch ($ch) {
            '"'  { [void]$sb.Append('\"') }
            '\'  { [void]$sb.Append('\\') }
            "`n" { [void]$sb.Append('\n') }
            "`r" { [void]$sb.Append('\r') }
            "`t" { [void]$sb.Append('\t') }
            default {
                if ([int]$ch -lt 32) { [void]$sb.Append(('\u{0:x4}' -f [int]$ch)) }
                else                 { [void]$sb.Append($ch) }
            }
        }
    }
    [void]$sb.Append('"')
    return $sb.ToString()
}

function Get-HttpProviderResults {
    <#
      Relay the search to ANY local/self-hosted search service that speaks the same tiny
      contract GobboNet already uses, and return [{title, url, content}].

      WHY A GENERIC SEAM AND NOT A BUNDLED SEARCH ENGINE. The first draft of this file
      hand-rolled a DuckDuckGo client here. It was measurably bad: html.duckduckgo.com answers
      a plain client with HTTP 202 and a bot interstitial containing no results at all, and the
      Instant Answer API — which does work without a key — is an encyclopedia, not an index.
      Measured on three ordinary queries it answered ONE ("llama.cpp") and returned nothing for
      "gguf quantization" and "what is a transformer model".

      A maintained client library (the `ddgs` package, for instance) answered all three with
      real web results on the same machine, same network, same minute. Read that the right way
      round: KEYLESS SEARCH WORKS. The hard part was never the absent API key — it is tracking
      a search engine's changing shape, and a library whose job that is already does it. That
      is why one succeeded where the hand-rolled version failed, on the same network, minutes
      apart.

      What a chat app should not do is carry its own copy of that maintenance, because it goes
      stale silently: a broken scraper returns an empty list rather than an error, so the UI
      says "found nothing" for as long as nobody checks.

      So this proxy does not implement search. It forwards to something whose job that is —
      and since every maintained client is a Python or Node package while this tree is
      PowerShell and a browser, forwarding is also the only way to reach one at all.
      Point SEARCH_URL at anything that accepts {"query","max_results"} and returns
      {"results":[{title,url,content}]} — a local SearxNG, your own service, whatever you run.
      Nothing here is tied to a vendor, and no key is required unless yours needs one.
    #>
    param([string]$Query, [int]$MaxResults = 5, [string]$Url, [string]$Auth)

    if (-not $Url) { throw 'SEARCH_URL is not set' }
    try {
        $payload = '{"query":' + (ConvertTo-JsonString $Query) + ',"max_results":' + $MaxResults + '}'
        $headers = @{ 'Content-Type' = 'application/json' }
        if ($Auth) { $headers['Authorization'] = $Auth }
        $r = Invoke-WebRequest -Uri $Url -Method POST -Body $payload -Headers $headers `
                               -UseBasicParsing -TimeoutSec 25
        if ($r.StatusCode -ne 200) { return @() }
        $j = $r.Content | ConvertFrom-Json -ErrorAction Stop
        $out = @()
        foreach ($item in $j.results) {
            if ($out.Count -ge $MaxResults) { break }
            # Accept the three names search backends actually use for the snippet.
            # SearxNG says `content`, most engines say `snippet`, some say
            # `description`. Reading only `content` is how a working provider
            # returns rows with an EMPTY body: titles and links render, every
            # snippet is blank, and it reads as "the model ignored the results"
            # rather than a field-name mismatch. Same silent-empty shape this
            # file's other comments are about, one level down.
            $body = $item.content
            if (-not $body) { $body = $item.snippet }
            if (-not $body) { $body = $item.description }
            $out += [pscustomobject]@{
                title   = [string]$item.title
                url     = [string]$item.url
                content = [string]$body
            }
        }
        return $out
    } catch {
        # Search being down must never take chat down: webSearch() already reads null/empty as
        # "nothing found" and carries on.
        return @()
    }
}

$listener = New-Object System.Net.HttpListener
$listener.Prefixes.Add("http://127.0.0.1:$Port/")
try { $listener.Start() } catch { exit 1 }

while ($listener.IsListening) {
    $ctx  = $listener.GetContext()
    $resp = $ctx.Response
    $resp.AddHeader('Access-Control-Allow-Origin',  '*')
    $resp.AddHeader('Access-Control-Allow-Methods', 'POST, GET, OPTIONS')
    $resp.AddHeader('Access-Control-Allow-Headers', 'Content-Type, Authorization')

    if ($ctx.Request.HttpMethod -eq 'OPTIONS') {
        $resp.StatusCode = 204; $resp.Close(); continue
    }

    $path = $ctx.Request.Url.AbsolutePath

    if ($path -eq '/health') {
        # Reports the ACTIVE PROVIDER, so "is search working" and "which search" are one
        # question with one answer. The UI only needs status:ok, so this stays compatible.
        Write-Json $resp ('{"status":"ok","provider":' + (ConvertTo-JsonString $Provider) + '}')
        continue
    }

    try {
        $sr   = New-Object IO.StreamReader($ctx.Request.InputStream)
        $body = $sr.ReadToEnd(); $sr.Close()

        if ($Provider -eq 'http' -and $path -like '*web_search*') {
            # PARSE THE BODY AS JSON, because it is JSON. An earlier version pulled the
            # query out with a regex containing an escaped backslash class; PowerShell threw
            # "Unterminated [] set", $ErrorActionPreference=SilentlyContinue swallowed it, the
            # query came through EMPTY, and the provider dutifully returned no results. Search
            # looked broken when the parse was broken -- so do not hand-roll what ConvertFrom-Json
            # already does correctly, including embedded quotes and unicode.
            $query = ''
            $max   = 5
            try {
                # -ErrorAction Stop is REQUIRED here: the script-level SilentlyContinue
                # makes ConvertFrom-Json return $null instead of throwing, so the catch
                # below never fires and a malformed body searches for the empty string --
                # reported to the user as 'no results' rather than 'your request was bad'.
                $req = $body | ConvertFrom-Json -ErrorAction Stop
                if ($null -eq $req) { throw 'empty or unparseable body' }
                if ($req.query)       { $query = [string]$req.query }
                if ($req.max_results) { $max   = [int]$req.max_results }
            } catch {
                # Malformed body: report it rather than silently searching for nothing.
                Write-Json $resp '{"error":"proxy: request body was not valid JSON"}' 400
                continue
            }

            $items = Get-HttpProviderResults -Query $query -MaxResults $max -Url $SearchUrl -Auth $ctx.Request.Headers['Authorization']
            $parts = foreach ($r in $items) {
                '{"title":'   + (ConvertTo-JsonString $r.title) +
                ',"url":'     + (ConvertTo-JsonString $r.url) +
                ',"content":' + (ConvertTo-JsonString $r.content) + '}'
            }
            Write-Json $resp ('{"results":[' + ($parts -join ',') + ']}')
            continue
        }

        # Default: relay to Ollama exactly as before, forwarding the caller's key untouched.
        $targetUrl = 'https://ollama.com/api' + $path
        $headers   = @{ 'Content-Type' = 'application/json' }
        $auth      = $ctx.Request.Headers['Authorization']
        if ($auth) { $headers['Authorization'] = $auth }

        $wr = Invoke-WebRequest -Uri $targetUrl -Method POST -Body $body -Headers $headers `
                                -UseBasicParsing -TimeoutSec 30
        Write-Json $resp $wr.Content
    } catch {
        $msg = $_.Exception.Message.Replace('"', '').Replace("`r", '').Replace("`n", ' ')
        Write-Json $resp ('{"error":"proxy: ' + $msg + '"}') 502
    }
}
