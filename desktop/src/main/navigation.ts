export function isAdminURL(url: string, origin: string): boolean {
  if (!URL.canParse(url)) return false;
  const parsed = new URL(url);
  return (
    parsed.origin === origin && parsed.username === '' && parsed.password === ''
  );
}

export function isExternalURL(url: string): boolean {
  if (!URL.canParse(url)) return false;
  const parsed = new URL(url);
  return parsed.protocol === 'https:' || parsed.protocol === 'http:';
}
