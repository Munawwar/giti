function __giti_refs
    command git for-each-ref --format='%(refname:short)' refs/heads refs/remotes refs/tags 2>/dev/null |
        string match --invert --regex '/HEAD$'
end

complete --command giti --short-option f --long-option foreground --description 'Run in the foreground'
complete --command giti --short-option 1 --description 'Restart the resident'
complete --command giti --no-files --arguments '(__giti_refs) HEAD' --description 'Git revision'
