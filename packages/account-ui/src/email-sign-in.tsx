"use client";
import { useEffect, useRef, useState } from "react";
import { Button } from "./ui";

const codeLength = 6;
const digits = (text: string) => text.normalize("NFKC").replace(/[^0-9]/g, "");

function CodeInput({
  value,
  onChange,
  disabled,
}: {
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
}) {
  const ref = useRef<HTMLInputElement>(null);
  const [selection, setSelection] = useState([value.length, value.length]);
  useEffect(() => {
    ref.current?.focus();
  }, []);
  return (
    <div className="sign-in-code">
      {Array.from({ length: codeLength }, (_, i) => (
        <span
          key={i}
          aria-hidden="true"
          data-active={
            selection[0] === selection[1]
              ? i === Math.min(selection[0], codeLength - 1)
              : i >= selection[0] && i < selection[1]
          }
        >
          {value[i]}
        </span>
      ))}
      <input
        ref={ref}
        id="sign-in-code"
        type="text"
        inputMode="numeric"
        autoComplete="one-time-code"
        dir="ltr"
        required
        pattern="[0-9]{6}"
        disabled={disabled}
        value={value}
        onSelect={(event) => {
          const field = event.currentTarget;
          setSelection([field.selectionStart ?? 0, field.selectionEnd ?? 0]);
        }}
        onClick={(event) => {
          const field = event.currentTarget;
          const cells = Array.from(
            field.parentElement!.querySelectorAll("span"),
          );
          const position = cells.findIndex(
            (cell) => event.clientX < cell.getBoundingClientRect().right,
          );
          const index = Math.min(
            position < 0 ? codeLength : position,
            value.length,
          );
          field.setSelectionRange(index, Math.min(index + 1, value.length));
        }}
        onPaste={(event) => {
          const pasted = digits(event.clipboardData.getData("text"));
          if (pasted.length < codeLength) return;
          event.preventDefault();
          onChange(pasted.slice(0, codeLength));
          requestAnimationFrame(() =>
            ref.current?.setSelectionRange(codeLength, codeLength),
          );
        }}
        onChange={(event) =>
          onChange(digits(event.target.value).slice(0, codeLength))
        }
      />
    </div>
  );
}

export function EmailSignIn({
  busy,
  setBusy,
  request,
  methods,
}: {
  busy: boolean;
  setBusy: (value: boolean) => void;
  methods: string[];
  request: (
    action: "send" | "verify" | "password",
    email: string,
    secret: string,
  ) => Promise<void>;
}) {
  const [email, setEmail] = useState("");
  const [otp, setOtp] = useState("");
  const [password, setPassword] = useState("");
  const [step, setStep] = useState<"send" | "verify" | "password">(
    methods.includes("email") ? "send" : "password",
  );
  const [error, setError] = useState("");
  const [sent, setSent] = useState(false);
  const [working, setWorking] = useState("");
  const validEmail =
    /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim()) &&
    email.trim().length <= 254;
  function changeStep(next: typeof step) {
    setStep(next);
    setError("");
    setOtp("");
    setPassword("");
    setSent(false);
  }
  async function submit(action: typeof step) {
    if (
      busy ||
      !validEmail ||
      (action === "verify" && otp.length !== codeLength) ||
      (action === "password" && !password)
    )
      return;
    setError("");
    setSent(false);
    setBusy(true);
    setWorking(action);
    try {
      await request(
        action,
        email.trim(),
        action === "password" ? password : otp,
      );
      if (action === "send") {
        changeStep("verify");
        setSent(true);
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "请求失败，请重试。");
    } finally {
      setWorking("");
      setBusy(false);
    }
  }
  return (
    <form
      className="email-sign-in"
      autoCapitalize="none"
      onSubmit={(event) => {
        event.preventDefault();
        void submit(step);
      }}
    >
      {step === "verify" ? (
        <>
          <div className="sign-in-recipient">
            <span>{email.trim()}</span>
            <Button
              variant="ghost"
              size="small"
              disabled={busy}
              onClick={() => changeStep("send")}
            >
              更换邮箱
            </Button>
          </div>
          <label htmlFor="sign-in-code">邮箱验证码</label>
          <CodeInput
            value={otp}
            disabled={busy}
            onChange={(value) => {
              setOtp(value);
              setError("");
            }}
          />
        </>
      ) : (
        <>
          <label htmlFor="sign-in-email">邮箱地址</label>
          <input
            className="field"
            id="sign-in-email"
            type="email"
            required
            autoComplete="email"
            placeholder="name@example.com"
            disabled={busy}
            value={email}
            onChange={(e) => {
              setEmail(e.target.value);
              setError("");
            }}
          />
          {step === "password" && (
            <>
              <label htmlFor="sign-in-password">密码</label>
              <input
                className="field"
                id="sign-in-password"
                type="password"
                required
                autoComplete="current-password"
                disabled={busy}
                value={password}
                onChange={(e) => {
                  setPassword(e.target.value);
                  setError("");
                }}
              />
            </>
          )}
        </>
      )}
      {error && (
        <p className="account-message" role="alert">
          {error}
        </p>
      )}
      {sent && (
        <p className="muted" role="status">
          验证码已发送，请查看邮箱。
        </p>
      )}
      <Button
        type="submit"
        variant="primary"
        aria-busy={Boolean(working)}
        disabled={
          busy ||
          !validEmail ||
          (step === "verify" && otp.length !== codeLength) ||
          (step === "password" && !password)
        }
      >
        {working === "send"
          ? "发送中…"
          : working
            ? "登录中…"
            : step === "send"
              ? "发送验证码"
              : "登录"}
      </Button>
      {step === "verify" ? (
        <Button variant="ghost" disabled={busy} onClick={() => submit("send")}>
          重新发送
        </Button>
      ) : (
        <div className="sign-in-methods">
          {step === "send" && (
            <Button
              variant="ghost"
              disabled={busy || !validEmail}
              onClick={() => changeStep("verify")}
            >
              已有验证码
            </Button>
          )}
          {methods.includes("password") && methods.includes("email") && (
            <Button
              variant="ghost"
              disabled={busy}
              onClick={() =>
                changeStep(step === "password" ? "send" : "password")
              }
            >
              {step === "password" ? "用验证码登录" : "用密码登录"}
            </Button>
          )}
        </div>
      )}
    </form>
  );
}
