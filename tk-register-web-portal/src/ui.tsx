import { Eye, EyeOff, LoaderCircle } from 'lucide-react';
import {
  useId,
  useState,
  type InputHTMLAttributes,
  type ReactNode,
} from 'react';

export function ErrorNotice({ children }: { children: ReactNode }) {
  return children ? (
    <div className='notice error' role='alert'>
      {children}
    </div>
  ) : null;
}
export function Loading({ label = 'Loading…' }: { label?: string }) {
  return (
    <div className='loading' role='status'>
      <LoaderCircle className='spin' aria-hidden='true' />
      {label}
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
      <label htmlFor={id}>{label}</label>
      <div className='password-field'>
        <input {...props} id={id} type={visible ? 'text' : 'password'} />
        <button
          className='eye-button'
          type='button'
          aria-label={`${visible ? 'Hide' : 'Show'} ${label.toLowerCase()}`}
          aria-pressed={visible}
          onClick={() => setVisible(!visible)}
        >
          {visible ? <EyeOff aria-hidden='true' /> : <Eye aria-hidden='true' />}
        </button>
      </div>
    </div>
  );
}
