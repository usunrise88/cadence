// A reviewer's invitation link is /#invitation=cri_…: the token rides in the fragment, which browsers never send to a
// server or put in a Referer, and the page removes it from the address bar once read.

/** The invitation token of a location hash, if any. */
export function invitationToken(hash: string): string | undefined {
  const m = /(?:^#|&)invitation=(cri_[a-z0-9]+)/i.exec(hash);
  return m?.[1];
}
