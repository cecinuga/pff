// Firebase multi-factor helpers (TOTP authenticator apps and SMS).
// E-mail codes are handled by the backend (see pages/Gates.tsx, Settings.tsx).
import {
  getMultiFactorResolver,
  multiFactor,
  PhoneAuthProvider,
  PhoneMultiFactorGenerator,
  RecaptchaVerifier,
  TotpMultiFactorGenerator,
  type Auth,
  type MultiFactorError,
  type MultiFactorResolver,
  type TotpSecret,
  type User,
} from "firebase/auth";

export type { MultiFactorResolver, TotpSecret };

export function isMfaRequiredError(e: unknown): e is MultiFactorError {
  return typeof e === "object" && e !== null && (e as { code?: string }).code === "auth/multi-factor-auth-required";
}

export function resolverFor(auth: Auth, e: MultiFactorError): MultiFactorResolver {
  return getMultiFactorResolver(auth, e);
}

let recaptcha: RecaptchaVerifier | null = null;

/** Invisible reCAPTCHA required by Firebase for SMS; containerId must exist in the DOM. */
function verifier(auth: Auth, containerId: string): RecaptchaVerifier {
  if (!recaptcha) recaptcha = new RecaptchaVerifier(auth, containerId, { size: "invisible" });
  return recaptcha;
}

export function resetRecaptcha() {
  recaptcha?.clear();
  recaptcha = null;
}

// ---- sign-in with a second factor --------------------------------------------

export async function resolveWithTotp(resolver: MultiFactorResolver, hintUid: string, code: string) {
  const assertion = TotpMultiFactorGenerator.assertionForSignIn(hintUid, code);
  return resolver.resolveSignIn(assertion);
}

export async function sendSignInSms(auth: Auth, resolver: MultiFactorResolver, hintIndex: number, containerId: string): Promise<string> {
  const hint = resolver.hints[hintIndex];
  if (!hint) throw new Error("Unknown second factor");
  return new PhoneAuthProvider(auth).verifyPhoneNumber({ multiFactorHint: hint, session: resolver.session }, verifier(auth, containerId));
}

export async function resolveWithSms(resolver: MultiFactorResolver, verificationId: string, code: string) {
  const cred = PhoneAuthProvider.credential(verificationId, code);
  return resolver.resolveSignIn(PhoneMultiFactorGenerator.assertion(cred));
}

// ---- enrolment ----------------------------------------------------------------

export function enrolledFactors(user: User) {
  return multiFactor(user).enrolledFactors;
}

export async function startTotpEnrollment(user: User): Promise<TotpSecret> {
  const session = await multiFactor(user).getSession();
  return TotpMultiFactorGenerator.generateSecret(session);
}

export async function finishTotpEnrollment(user: User, secret: TotpSecret, code: string) {
  const assertion = TotpMultiFactorGenerator.assertionForEnrollment(secret, code);
  await multiFactor(user).enroll(assertion, "Authenticator app");
}

export async function startSmsEnrollment(auth: Auth, user: User, phoneNumber: string, containerId: string): Promise<string> {
  const session = await multiFactor(user).getSession();
  return new PhoneAuthProvider(auth).verifyPhoneNumber({ phoneNumber, session }, verifier(auth, containerId));
}

export async function finishSmsEnrollment(user: User, verificationId: string, code: string) {
  const cred = PhoneAuthProvider.credential(verificationId, code);
  await multiFactor(user).enroll(PhoneMultiFactorGenerator.assertion(cred), "Phone");
}

export async function unenroll(user: User, factorUid: string) {
  await multiFactor(user).unenroll(factorUid);
}

/** Maps Firebase error codes to sentences a user can act on. */
export function firebaseMessage(e: unknown): string {
  const code = (e as { code?: string })?.code ?? "";
  switch (code) {
    case "auth/requires-recent-login":
      return "For your security, sign out and sign in again, then retry.";
    case "auth/invalid-verification-code":
    case "auth/invalid-verification-id":
      return "The code is not valid. Check it and try again.";
    case "auth/popup-closed-by-user":
    case "auth/cancelled-popup-request":
      return "Sign-in was cancelled.";
    case "auth/popup-blocked":
      return "The browser blocked the sign-in popup. Allow popups for this site.";
    case "auth/operation-not-allowed":
      return "This second factor is not enabled for the project (requires Identity Platform).";
    case "auth/unverified-email":
      return "Verify your e-mail address before adding a second factor.";
    default:
      return e instanceof Error ? e.message : String(e);
  }
}
