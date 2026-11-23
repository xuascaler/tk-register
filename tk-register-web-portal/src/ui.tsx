import { Eye, EyeOff, LoaderCircle } from 'lucide-react';
import { t, translateMessage } from './i18n';
import { APIError, localizedError } from './api';
import {
  useId,
  useState,
  type InputHTMLAttributes,
  type ReactNode,
} from 'react';

export function ErrorNotice({ children }: { children: ReactNode | APIError }) {
  return children ? (
    <div className='notice error' role='alert'>
      {typeof children === 'string' || children instanceof APIError ? localizedError(children) : children}
    </div>
  ) : null;
}
export function Loading({ label = 'Loading…' }: { label?: string }) {
  return (
    <div className='loading' role='status'>
      <LoaderCircle className='spin' aria-hidden='true' />
      {translateMessage(label)}
    </div>
  );
}
export function PasswordField({
  label,
  ...props
}: InputHTMLAttributes<HTMLInputElement> & { label: string }) {
  const [visible, setVisible] = useState(false);
  const id = useId();
  return (
    <div className='field'>
      <label htmlFor={id}>{translateMessage(label)}</label>
      <div className='password-field'>
        <input {...props} id={id} type={visible ? 'text' : 'password'} />
        <button
          className='eye-button'
          type='button'
          aria-label={t(visible ? 'Hide {{label}}' : 'Show {{label}}', { label: translateMessage(label).toLowerCase() })}
          aria-pressed={visible}
          onClick={() => setVisible(!visible)}
        >
          {visible ? <EyeOff aria-hidden='true' /> : <Eye aria-hidden='true' />}
        </button>
      </div>
    </div>
  );
}
