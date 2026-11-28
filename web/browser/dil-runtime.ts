export function isGatewayDILRunner(url: URL, origin: string, base: string) {
  return (
    typeof base === 'string' &&
    /^\/cwb\/dil\/[a-f0-9]{64}\/$/.test(base) &&
    url.origin === origin &&
    !url.username &&
    !url.password &&
    !url.search &&
    url.pathname === base + 'deployments/dil/v15/runner.html'
  );
}
