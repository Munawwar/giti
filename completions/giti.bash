_giti()
{
    local current="${COMP_WORDS[COMP_CWORD]}" ref
    local -a refs candidates

    while IFS= read -r ref; do
        [[ $ref == */HEAD ]] || refs+=("$ref")
    done < <(git for-each-ref --format='%(refname:short)' refs/heads refs/remotes refs/tags 2>/dev/null)

    case $COMP_CWORD in
        1) candidates=(-f --foreground -1 HEAD "${refs[@]}") ;;
        2)
            case ${COMP_WORDS[1]} in
                -f|--foreground) candidates=(HEAD "${refs[@]}") ;;
            esac
            ;;
    esac

    COMPREPLY=()
    for ref in "${candidates[@]}"; do
        [[ $ref == "$current"* ]] && COMPREPLY+=("$ref")
    done
}

complete -F _giti giti
