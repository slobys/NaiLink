import aliyun from '~/assets/brands/aliyun.ico?url'
import github from '~/assets/brands/github-black.svg'
import githubDark from '~/assets/brands/github-white.svg'
import google from '~/assets/brands/google.svg'
import openai from '~/assets/brands/openai.svg'

/** Original brand assets, bundled locally so router connectivity is not required. */
export function ServiceLogo({ service }: { service: string }) {
  return (
    <span className="service-logo" aria-hidden="true">
      {service === 'github' ? (
        <>
          <img src={github} className="dark:hidden" width="24" height="24" alt="" />
          <img src={githubDark} className="hidden dark:block" width="24" height="24" alt="" />
        </>
      ) : (
        <img src={{ aliyun, google, openai }[service]} width="24" height="24" alt="" />
      )}
    </span>
  )
}
